package simulation

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"rzdarctica.local/backend/internal/core/jsonutil"
	"rzdarctica.local/backend/internal/features/calculations"
)

type JobArgs struct {
	RunID     string `json:"run_id"`
	AttemptID string `json:"attempt_id"`
}

func (JobArgs) Kind() string { return "rzd_simulation_calculation" }

type Worker struct {
	river.WorkerDefaults[JobArgs]
	store *Store
}

func NewWorker(store *Store) *Worker { return &Worker{store: store} }

func (*Worker) Timeout(*river.Job[JobArgs]) time.Duration { return 20 * time.Second }

type attempt struct {
	Revision int
	State    int
	Version  int
	Hash     string
	Payload  json.RawMessage
	Status   string
}

func readAttempt(ctx context.Context, tx pgx.Tx, args JobArgs) (attempt, error) {
	var a attempt
	err := tx.QueryRow(ctx, `SELECT condition_revision,state_version,plan_version,input_sha256,payload,status
		FROM rzd_sim_attempts WHERE id=$1 AND run_id=$2`, args.AttemptID, args.RunID).
		Scan(&a.Revision, &a.State, &a.Version, &a.Hash, &a.Payload, &a.Status)
	return a, err
}

func current(r *storedRun, a attempt, args JobArgs) bool {
	return r.AttemptID == args.AttemptID && r.ConditionRevision == a.Revision && r.StateVersion == a.State && r.PlanVersion == a.Version
}

func (w *Worker) Work(ctx context.Context, job *river.Job[JobArgs]) error {
	args := job.Args
	var input attempt
	var partyIDs []string
	proceed := false
	_, err := w.store.update(ctx, args.RunID, func(tx pgx.Tx, r *storedRun) error {
		var err error
		input, err = readAttempt(ctx, tx, args)
		if err != nil || input.Status != "queued" && input.Status != "running" {
			return err
		}
		if !current(r, input, args) {
			_, err = tx.Exec(ctx, "UPDATE rzd_sim_attempts SET status='stale' WHERE id=$1", args.AttemptID)
			return err
		}
		proceed = true
		partyIDs = r.partyIDs()
		if r.Status != "running" {
			r.record("calculation_started", fmt.Sprintf("Идёт расчёт · редакция %d", r.ConditionRevision))
		}
		r.Status, r.Message = "running", "CP-SAT рассчитывает план."
		_, err = tx.Exec(ctx, "UPDATE rzd_sim_attempts SET status='running' WHERE id=$1", args.AttemptID)
		return err
	})
	if err != nil || !proceed {
		return err
	}

	status, message := "compute_error", "Вычислительный сервис недоступен или отклонил снимок. Расчёт можно повторить."
	var response calculations.ComputeResponse
	var solved result
	digest, err := jsonutil.SHA256(input.Payload)
	var snapshot struct {
		ID string `json:"snapshot_id"`
	}
	if err == nil && digest == input.Hash && json.Unmarshal(input.Payload, &snapshot) == nil {
		response, err = w.store.computer.Solve(ctx, calculations.ComputeRequest{RequestID: args.AttemptID, EngineProfile: profile, Payload: input.Payload})
		if err == nil {
			status, message = classify(args.AttemptID, snapshot.ID, input.Hash, response, &solved, partyIDs)
		}
	}

	persist, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	_, err = w.store.update(persist, args.RunID, func(tx pgx.Tx, r *storedRun) error {
		a, err := readAttempt(persist, tx, args)
		if err != nil || a.Status != "running" {
			return err
		}
		if !current(r, a, args) || a.Hash != input.Hash {
			status, message = "stale", "Устарел · изменился снимок прогона."
			r.record("calculation_stale", fmt.Sprintf("Редакция %d · %s", a.Revision, message))
			return writeOutcome(persist, tx, args.AttemptID, status, message, response.Result)
		}
		r.Status, r.Message = status, message
		if status == "candidate" {
			r.Candidate = &candidate{ID: args.AttemptID, Status: solved.Status, Plan: solved.Plan, Message: message}
		}
		if status == "conflict" {
			for _, conflict := range solved.Conflicts {
				for i := range r.Events {
					e := &r.Events[i]
					if e.ID == conflict.EventID && e.Source == conflict.Source {
						e.Status, e.Message = "conflict", message
					}
				}
			}
		}
		r.record("calculation_result", fmt.Sprintf("Редакция %d · %s", r.ConditionRevision, message))
		return writeOutcome(persist, tx, args.AttemptID, status, message, response.Result)
	})
	return err
}

func writeOutcome(ctx context.Context, tx pgx.Tx, id, status, message string, result json.RawMessage) error {
	_, err := tx.Exec(ctx, "UPDATE rzd_sim_attempts SET status=$2,message=$3,result=$4 WHERE id=$1", id, status, message, result)
	return err
}

func classify(id, snapshot, hash string, response calculations.ComputeResponse, solved *result, partyIDs []string) (string, string) {
	if response.RequestID != id || response.EngineProfile != profile || response.InputSHA256 != hash ||
		response.ValidationErrors == nil || len(response.ValidationErrors) != 0 || json.Unmarshal(response.Result, solved) != nil ||
		solved.EngineProfile != profile || solved.InputSHA256 != hash || solved.SnapshotID != snapshot ||
		solved.CalculationID != id || solved.AttemptID != id {
		return "compute_error", "Результат расчёта не прошёл проверку снимка и ограничений."
	}
	if solved.Status == "OPTIMAL" || solved.Status == "FEASIBLE" {
		var assignments []json.RawMessage
		if !solved.Plan.covers(partyIDs) || json.Unmarshal(solved.Assignments, &assignments) != nil || assignments == nil {
			return "compute_error", "Расчётный сервис вернул неполный кандидат."
		}
		if solved.Status == "OPTIMAL" {
			return "candidate", "Оптимальный план для учебной модели. Проверка пройдена."
		}
		return "candidate", "Допустимый план. Проверка пройдена."
	}
	if solved.Plan != nil {
		return "compute_error", "Расчётный сервис вернул противоречивый статус."
	}
	switch solved.Status {
	case "INFEASIBLE":
		return "infeasible", "Допустимого плана нет при текущих ограничениях, рейсах и сроках."
	case "UNKNOWN":
		return "unknown", "Расчёт не завершён. Поиск можно повторить."
	case "CONFLICT":
		message := "Новое условие конфликтует с начатой физической операцией. Требуется разбор."
		if len(solved.Conflicts) != 0 {
			message += " Операция " + solved.Conflicts[0].OperationID + "."
		}
		return "conflict", message
	default:
		return "compute_error", "Расчётный сервис вернул неизвестный результат."
	}
}

var _ river.Worker[JobArgs] = (*Worker)(nil)
