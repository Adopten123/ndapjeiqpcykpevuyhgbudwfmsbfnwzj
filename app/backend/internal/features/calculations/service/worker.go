package service

import (
	"context"
	"errors"
	"time"

	"github.com/riverqueue/river"
	"rzdarctica.local/backend/internal/features/calculations"
)

type WorkerStore interface {
	BeginAttempt(context.Context, string) (calculations.Calculation, bool, error)
	Succeed(context.Context, string, []byte) error
	RetryOrFail(context.Context, string, bool, string) error
}

type Solver interface {
	Solve(context.Context, calculations.ComputeRequest) (calculations.ComputeResponse, error)
}

type Worker struct {
	river.WorkerDefaults[calculations.JobArgs]
	store  WorkerStore
	solver Solver
}

func NewWorker(store WorkerStore, solver Solver) *Worker {
	return &Worker{store: store, solver: solver}
}

func (w *Worker) Timeout(*river.Job[calculations.JobArgs]) time.Duration { return 20 * time.Second }

// Короткий интервал ограниченных учебных повторов, не эксплуатационный SLA.
func (w *Worker) NextRetry(*river.Job[calculations.JobArgs]) time.Time {
	return time.Now().Add(time.Second)
}

func (w *Worker) Work(ctx context.Context, job *river.Job[calculations.JobArgs]) error {
	c, proceed, err := w.store.BeginAttempt(ctx, job.Args.CalculationID)
	if err != nil || !proceed {
		return err
	}
	response, err := w.solver.Solve(ctx, calculations.ComputeRequest{RequestID: c.ID, EngineProfile: c.EngineProfile, Payload: c.Payload})
	if err != nil {
		var remote *ComputeHTTPError
		permanent := errors.As(err, &remote) && remote.Status >= 400 && remote.Status < 500 && remote.Status != 408 && remote.Status != 429
		terminal := permanent || job.Attempt >= job.MaxAttempts
		message := "Расчётный сервис недоступен. Будет повторная попытка."
		if terminal {
			message = "Расчёт не выполнен: сервис недоступен или отклонил вход."
		}
		if saveErr := w.store.RetryOrFail(ctx, c.ID, terminal, message); saveErr != nil {
			return saveErr
		}
		if terminal {
			return nil
		}
		return calculations.ErrCompute
	}
	if err := ValidateResponse(c, response); err != nil {
		return w.store.RetryOrFail(ctx, c.ID, true, "Результат расчёта не прошёл проверку снимка и контекста.")
	}
	return w.store.Succeed(ctx, c.ID, response.Result)
}
