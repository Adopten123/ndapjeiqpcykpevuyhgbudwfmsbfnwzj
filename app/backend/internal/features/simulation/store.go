package simulation

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"rzdarctica.local/backend/internal/core/jsonutil"
	"rzdarctica.local/backend/internal/features/calculations"
)

//go:embed schema.sql
var schema string

var (
	errMissing = errors.New("Учебный прогон не найден.")
	errStale   = errors.New("Прогон изменился. Обновите данные и повторите действие.")
	errBlocked = errors.New("Это действие недоступно в текущем состоянии прогона.")
	errSeed    = errors.New("Не удалось получить проверенный исходный план S1.")
)

type computer interface {
	Snapshot(context.Context, string) (calculations.Snapshot, error)
	Solve(context.Context, calculations.ComputeRequest) (calculations.ComputeResponse, error)
}

type Store struct {
	pool     *pgxpool.Pool
	queue    *river.Client[pgx.Tx]
	computer computer
	feed     *feedClient
}

func New(pool *pgxpool.Pool, queue *river.Client[pgx.Tx], compute computer, sourceURL string) *Store {
	return &Store{pool, queue, compute, newFeed(sourceURL)}
}

func (s *Store) Migrate(ctx context.Context) error {
	_, err := s.pool.Exec(ctx, schema)
	return err
}

type rowQuery interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func get(ctx context.Context, db rowQuery, id string, lock bool) (storedRun, error) {
	var value storedRun
	if !uuidPattern.MatchString(id) {
		return value, errMissing
	}
	query := "SELECT state FROM rzd_sim_runs WHERE id=$1"
	if lock {
		query += " FOR UPDATE"
	}
	var data []byte
	err := db.QueryRow(ctx, query, id).Scan(&data)
	if errors.Is(err, pgx.ErrNoRows) {
		return value, errMissing
	}
	if err == nil {
		err = json.Unmarshal(data, &value)
		value.restoreMode()
		value.restoreConfiguration()
	}
	return value, err
}

func save(ctx context.Context, tx pgx.Tx, r *storedRun) error {
	data, err := json.Marshal(r)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, "UPDATE rzd_sim_runs SET state=$2::jsonb WHERE id=$1", r.ID, data)
	return err
}

func (s *Store) update(ctx context.Context, id string, change func(pgx.Tx, *storedRun) error) (run, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return run{}, err
	}
	defer tx.Rollback(context.Background())
	r, err := get(ctx, tx, id, true)
	if err != nil {
		return run{}, err
	}
	if err = change(tx, &r); err == nil {
		err = save(ctx, tx, &r)
	}
	if err == nil {
		err = tx.Commit(ctx)
	}
	return r.run, err
}

func (s *Store) list(ctx context.Context) ([]summary, error) {
	rows, err := s.pool.Query(ctx, "SELECT state FROM rzd_sim_runs ORDER BY created_at DESC,id DESC LIMIT 100")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []summary{}
	for rows.Next() {
		var data []byte
		var item summary
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(data, &item); err != nil {
			return nil, err
		}
		item.restoreMode()
		values = append(values, item)
	}
	return values, rows.Err()
}

type createRequest struct {
	Name       string          `json:"name"`
	Mode       runMode         `json:"mode,omitempty"`
	Scenario   string          `json:"scenario"`
	CopyFrom   string          `json:"copy_from,omitempty"`
	PartyCount json.RawMessage `json:"party_count,omitempty"`
	RouteID    string          `json:"route_id,omitempty"`
	CargoID    string          `json:"cargo_id,omitempty"`
	CargoName  string          `json:"cargo_name,omitempty"`
}

func (r *createRequest) validateConfiguration() (int, error) {
	var count int
	if len(r.PartyCount) != 0 {
		if err := json.Unmarshal(r.PartyCount, &count); err != nil || count < 1 || count > len(templatePartyOrder) {
			return 0, &fieldError{"party_count", "Введите целое число от 1 до 4."}
		}
	}
	if r.RouteID != "" && r.RouteID != pilotRouteID {
		return 0, &fieldError{"route_id", "Выберите доступный пилотный маршрут."}
	}
	if _, known := cargoNames[r.CargoID]; r.CargoID != "" && !known {
		return 0, &fieldError{"cargo_id", "Выберите тип сухого неопасного груза."}
	}
	r.CargoName = strings.TrimSpace(r.CargoName)
	if len([]rune(r.CargoName)) > 100 || strings.IndexFunc(r.CargoName, unicode.IsControl) >= 0 ||
		strings.ContainsAny(r.CargoName, "\u2028\u2029") {
		return 0, &fieldError{"cargo_name", "Укажите наименование до 100 символов в одной строке."}
	}
	return count, nil
}

func (r *createRequest) resolveMode() error {
	switch r.Mode {
	case "":
		if r.Scenario != "standard" && r.Scenario != "critical" && r.Scenario != "short-ice" {
			return &fieldError{"scenario", "Выберите режим запуска или учебный сценарий."}
		}
		r.Mode = modeLegacy
	case modeTraining, modeOperations:
		if r.Scenario != "" {
			return &fieldError{"scenario", "Режим запуска определяет источник событий."}
		}
		r.Scenario = "manual"
		if r.Mode == modeOperations {
			if r.CopyFrom != "" {
				return &fieldError{"copy_from", "Диспетчерский прогон получает новую ленту событий."}
			}
			r.Scenario = string(modeOperations)
		}
	default:
		return &fieldError{"mode", "Выберите учебный или диспетчерский режим."}
	}
	return nil
}

type sourceModel struct {
	Facts []struct {
		ID      string `json:"id"`
		PartyID string `json:"party_id"`
	} `json:"facts"`
	Operations []struct {
		operation
		State string `json:"state"`
	} `json:"operations"`
	Parties []struct {
		party
		Current struct {
			Location string `json:"location_id"`
		} `json:"current"`
	} `json:"parties"`
	Edges []struct {
		ID   string `json:"id"`
		Mode string `json:"mode"`
		From string `json:"from_location_id"`
		To   string `json:"to_location_id"`
	} `json:"edges"`
	Services []struct {
		ID string `json:"id"`
	} `json:"services"`
}

func (s *Store) initial(ctx context.Context, req createRequest, requestedCount int) (storedRun, error) {
	r := storedRun{run: run{
		summary: summary{ID: newID(), Name: req.Name, Mode: req.Mode, Scenario: req.Scenario, Status: "ready", PlanVersion: 1},
		Events:  []eventRecord{}, ManualEvents: []event{}, Operations: []operation{}, Parties: []party{},
		Targets: []target{}, Facts: []fact{}, Timeline: []entry{},
		Source: sourceState{Cursor: "0", Complete: -1},
	}, Seen: map[string]string{}}
	r.restoreConfiguration()
	count := len(templatePartyOrder)
	if req.CopyFrom != "" {
		old, err := get(ctx, s.pool, req.CopyFrom, false)
		if err != nil {
			return r, err
		}
		count = len(old.Parties)
		r.Cargo, r.Route = old.Cargo, old.Route
		for _, oldEvent := range old.Events {
			if oldEvent.Source == "manual" {
				r.ManualEvents = append(r.ManualEvents, event{eventInput: oldEvent.eventInput, RunID: r.ID, Source: "manual", ID: newID()})
			}
		}
		for _, oldEvent := range old.ManualEvents {
			r.ManualEvents = append(r.ManualEvents, event{eventInput: oldEvent.eventInput, RunID: r.ID, Source: "manual", ID: newID()})
		}
	}
	if requestedCount != 0 {
		count = requestedCount
	}
	if req.RouteID != "" {
		r.Route.ID = req.RouteID
	}
	if req.CargoID != "" {
		r.Cargo = cargoSelection{ID: req.CargoID, Name: cargoNames[req.CargoID]}
	}
	if req.CargoName != "" {
		r.Cargo.Name = req.CargoName
	}
	if count < 1 || count > len(templatePartyOrder) {
		return r, errSeed
	}
	selected := templatePartyOrder[:count]
	snapshot, err := s.computer.Snapshot(ctx, "S1")
	var model sourceModel
	if err != nil || json.Unmarshal(snapshot.Payload, &model) != nil || len(model.Parties) != len(templatePartyOrder) || len(model.Services) != 2 {
		return r, errSeed
	}
	id := newID()
	response, err := s.computer.Solve(ctx, calculations.ComputeRequest{RequestID: id, EngineProfile: calculations.Profile, Payload: snapshot.Payload})
	var solved result
	if err != nil || response.RequestID != id || response.EngineProfile != calculations.Profile || response.InputSHA256 != snapshot.InputSHA256 ||
		len(response.ValidationErrors) != 0 || json.Unmarshal(response.Result, &solved) != nil || solved.Status != "OPTIMAL" || solved.Plan == nil {
		return r, errSeed
	}
	for _, p := range model.Parties {
		if !slices.Contains(selected, p.ID) {
			continue
		}
		p.Location = p.Current.Location
		r.Parties = append(r.Parties, p.party)
	}
	if len(r.Parties) != count {
		return r, errSeed
	}
	for _, edge := range model.Edges {
		eventType := ""
		switch edge.Mode {
		case "rail":
			eventType = railClosure
		case "sea":
			eventType = seaClosure
		}
		if eventType != "" {
			r.Targets = append(r.Targets, target{edge.ID, eventType, edge.From + " → " + edge.To + " · " + edge.ID})
		}
	}
	for _, service := range model.Services {
		r.Targets = append(r.Targets, target{service.ID, vesselChange, "Морской рейс " + service.ID})
	}
	states := map[string]string{}
	for _, op := range model.Operations {
		if !slices.Contains(selected, op.PartyID) {
			continue
		}
		states[op.ID] = op.State
		r.Operations = append(r.Operations, op.operation)
	}
	if count < len(templatePartyOrder) {
		allowedFacts := map[string]bool{}
		for _, fact := range model.Facts {
			if slices.Contains(selected, fact.PartyID) {
				allowedFacts[fact.ID] = true
			}
		}
		if err := solved.restrictTo(selected, states, allowedFacts); err != nil {
			return r, errSeed
		}
	}
	r.BasePlan, r.ActivePlan, r.Assignments = *solved.Plan, *solved.Plan, solved.Assignments
	label := "Создан учебный прогон · исходный план S1"
	if req.Mode == modeOperations {
		label = "Создан диспетчерский прогон · исходный план S1"
	}
	r.record("run_created", label)
	for _, scheduled := range r.BasePlan.Operations {
		if state := states[scheduled.OperationID]; state != "planned" {
			r.Facts = append(r.Facts, fact{scheduled, state})
			r.record("initial_fact", "Исходный факт · "+scheduled.OperationID)
		}
	}
	return r, nil
}

func (s *Store) create(ctx context.Context, req createRequest, actor string) (run, error) {
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" || len([]rune(req.Name)) > 100 {
		return run{}, &fieldError{"name", "Укажите название прогона длиной до 100 символов."}
	}
	if err := req.resolveMode(); err != nil {
		return run{}, err
	}
	count, err := req.validateConfiguration()
	if err != nil {
		return run{}, err
	}
	r, err := s.initial(ctx, req, count)
	if err != nil {
		return run{}, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return run{}, err
	}
	defer tx.Rollback(context.Background())
	if _, err = tx.Exec(ctx, "INSERT INTO rzd_sim_runs (id,state,created_by) VALUES($1,'{}',$2)", r.ID, actor); err != nil {
		return run{}, err
	}
	if err = saveVersion(ctx, tx, &r, nil, actor); err == nil {
		err = s.advance(ctx, tx, &r, 0)
	}
	if err == nil {
		err = save(ctx, tx, &r)
	}
	if err == nil {
		err = tx.Commit(ctx)
	}
	return r.run, err
}

func saveVersion(ctx context.Context, tx pgx.Tx, r *storedRun, attempt *string, actor string) error {
	value, err := json.Marshal(r.ActivePlan)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO rzd_sim_plans(run_id,version,attempt_id,plan,assignments,actor_id,decision_tick)
		VALUES($1,$2,$3,$4,$5,$6,$7)`, r.ID, r.PlanVersion, attempt, value, r.Assignments, actor, r.Tick)
	return err
}

func (s *Store) advance(ctx context.Context, tx pgx.Tx, r *storedRun, tick int) error {
	var raw []json.RawMessage
	source := sourceState{Cursor: r.Source.Cursor, Complete: tick}
	if r.Mode != modeTraining {
		var err error
		raw, source, err = s.feed.read(ctx, r, tick)
		if err != nil {
			if r.Status != "source_error" {
				r.record("source_error", "Учебная лента недоступна · rail-mock / carrier-mock / ice-mock")
			}
			r.Status, r.Message = "source_error", "Учебная лента недоступна. Повторите получение данных с сохранённого курсора."
			return nil
		}
	}
	r.Tick, r.Source = tick, source
	r.StateVersion++
	r.Candidate, r.AttemptID = nil, ""
	r.complete()
	changed, conflict := r.reveal(raw)
	if changed {
		r.ConditionRevision++
	}
	if conflict {
		r.Status, r.Message = "conflict", "В ленте конфликтуют сообщения. Требуется разбор."
		r.record("conflict", r.Message)
		return nil
	}
	if changed {
		return s.enqueue(ctx, tx, r)
	}
	r.start()
	return nil
}

func (s *Store) step(ctx context.Context, id string, expected int) (run, error) {
	return s.update(ctx, id, func(tx pgx.Tx, r *storedRun) error {
		if r.StateVersion != expected {
			return errStale
		}
		if r.Status != "ready" && r.Status != "source_error" {
			return errBlocked
		}
		tick := r.nextTick()
		if r.Source.Complete < 0 {
			tick = r.Tick
		}
		return s.advance(ctx, tx, r, tick)
	})
}

func (s *Store) retry(ctx context.Context, id string, expected int) (run, error) {
	return s.update(ctx, id, func(tx pgx.Tx, r *storedRun) error {
		if r.StateVersion != expected {
			return errStale
		}
		if r.Status != "compute_error" && r.Status != "unknown" {
			return errBlocked
		}
		return s.enqueue(ctx, tx, r)
	})
}

func (s *Store) apply(ctx context.Context, id, candidateID, actor string) (run, error) {
	return s.update(ctx, id, func(tx pgx.Tx, r *storedRun) error {
		if r.AppliedID == candidateID {
			return nil
		}
		if r.Status != "candidate" || r.Candidate == nil || r.Candidate.ID != candidateID || r.Candidate.Plan == nil || r.AttemptID != candidateID {
			return errStale
		}
		var raw []byte
		var revision, state, version int
		if err := tx.QueryRow(ctx, `SELECT result,condition_revision,state_version,plan_version FROM rzd_sim_attempts
			WHERE id=$1 AND run_id=$2 AND status='candidate'`, candidateID, id).Scan(&raw, &revision, &state, &version); err != nil {
			return err
		}
		if revision != r.ConditionRevision || state != r.StateVersion || version != r.PlanVersion {
			return errStale
		}
		var solved result
		if err := json.Unmarshal(raw, &solved); err != nil || solved.Plan == nil {
			return errSeed
		}
		r.ActivePlan, r.Assignments = *solved.Plan, solved.Assignments
		r.PlanVersion++
		r.StateVersion++
		r.AppliedID, r.AttemptID, r.Candidate = candidateID, "", nil
		future := map[string]int{}
		for _, op := range r.ActivePlan.Operations {
			future[op.OperationID] = op.End
		}
		for i := range r.Facts {
			if r.Facts[i].State == "in_progress" {
				r.Facts[i].End = future[r.Facts[i].OperationID]
			}
		}
		r.record("plan_applied", fmt.Sprintf("Версия %d применена · %s", r.PlanVersion, actor))
		r.complete()
		r.start()
		return saveVersion(ctx, tx, r, &candidateID, actor)
	})
}

func (s *Store) manual(ctx context.Context, id, eventID, method string, input eventInput) (run, error) {
	return s.update(ctx, id, func(_ pgx.Tx, r *storedRun) error {
		index := -1
		for i, e := range r.ManualEvents {
			if e.ID == eventID {
				index = i
				break
			}
		}
		if method != "POST" && index < 0 {
			return errBlocked
		}
		if method == "DELETE" {
			r.ManualEvents = append(r.ManualEvents[:index], r.ManualEvents[index+1:]...)
			return nil
		}
		if err := r.validate(input); err != nil {
			return err
		}
		if r.Status == "completed" {
			return &fieldError{"available_tick", "Прогон завершён. Создайте копию сценария для новых событий."}
		}
		if *input.Available <= r.Tick {
			return &fieldError{"available_tick", "Ручное событие должно поступить на будущем шаге."}
		}
		if method == "POST" {
			if len(r.ManualEvents)+len(r.Events) >= 100 {
				return &fieldError{"available_tick", "Достигнут лимит учебных событий в прогоне."}
			}
			r.ManualEvents = append(r.ManualEvents, event{eventInput: input, RunID: id, Source: "manual", ID: newID()})
		} else {
			r.ManualEvents[index].eventInput = input
		}
		return nil
	})
}

func (s *Store) enqueue(ctx context.Context, tx pgx.Tx, r *storedRun) error {
	id := newID()
	events := []event{}
	for _, e := range r.Events {
		if e.Status == "applied" {
			events = append(events, e.event)
		}
	}
	input := map[string]any{
		"snapshot_id": newID(), "calculation_id": id, "attempt_id": id, "run_id": r.ID,
		"decision_tick": r.Tick, "condition_revision": r.ConditionRevision, "state_version": r.StateVersion, "plan_version": r.PlanVersion,
		"events": events, "fixed_operations": r.Facts,
		"baseline": map[string]any{"plan_id": fmt.Sprintf("SIM-v%d", r.PlanVersion), "plan": r.ActivePlan, "assignments": r.Assignments},
	}
	if len(r.Parties) < len(templatePartyOrder) {
		input["party_ids"] = r.partyIDs()
	}
	payload, err := json.Marshal(input)
	if err != nil {
		return err
	}
	hash, err := jsonutil.SHA256(json.RawMessage(payload))
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO rzd_sim_attempts(id,run_id,condition_revision,state_version,plan_version,input_sha256,payload,status)
		VALUES($1,$2,$3,$4,$5,$6,$7,'queued')`, id, r.ID, r.ConditionRevision, r.StateVersion, r.PlanVersion, hash, payload)
	if err != nil {
		return err
	}
	if _, err = s.queue.InsertTx(ctx, tx, JobArgs{RunID: r.ID, AttemptID: id}, &river.InsertOpts{Queue: Queue, MaxAttempts: 3}); err != nil {
		return err
	}
	r.Status, r.Message, r.AttemptID, r.Candidate = "queued", "Новые старты ждут проверенного плана.", id, nil
	r.record("calculation_queued", fmt.Sprintf("Расчёт поставлен в очередь · редакция %d", r.ConditionRevision))
	return nil
}
