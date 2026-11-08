package simulation

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"rzdarctica.local/backend/internal/core/jsonutil"
	"rzdarctica.local/backend/internal/features/calculations"
)

func TestCreateRequestResolvesModeAndLegacyScenario(t *testing.T) {
	for _, tc := range []struct {
		name     string
		input    createRequest
		mode     runMode
		scenario string
		field    string
	}{
		{name: "training", input: createRequest{Mode: modeTraining, CopyFrom: "saved"}, mode: modeTraining, scenario: "manual"},
		{name: "operations", input: createRequest{Mode: modeOperations}, mode: modeOperations, scenario: "operations"},
		{name: "legacy", input: createRequest{Scenario: "critical"}, mode: modeLegacy, scenario: "critical"},
		{name: "unknown mode", input: createRequest{Mode: "other"}, field: "mode"},
		{name: "mixed inputs", input: createRequest{Mode: modeTraining, Scenario: "critical"}, field: "scenario"},
		{name: "operations copy", input: createRequest{Mode: modeOperations, CopyFrom: "saved"}, field: "copy_from"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := tc.input
			err := req.resolveMode()
			if tc.field != "" {
				if issue, ok := err.(*fieldError); !ok || issue.field != tc.field {
					t.Fatalf("resolveMode() = %v, want field %s", err, tc.field)
				}
				return
			}
			if err != nil || req.Mode != tc.mode || req.Scenario != tc.scenario {
				t.Fatalf("resolveMode() = %+v, %v; want %s/%s", req, err, tc.mode, tc.scenario)
			}
		})
	}
}

func TestTrainingAdvancesWithoutMockAndLegacyRestoresMode(t *testing.T) {
	var old storedRun
	if err := json.Unmarshal([]byte(`{"scenario":"critical"}`), &old); err != nil {
		t.Fatal(err)
	}
	old.restoreMode()
	if old.Mode != modeLegacy || old.Scenario != "critical" {
		t.Fatalf("legacy mode and scenario changed: %+v", old.summary)
	}

	tick := 2
	r := storedRun{run: run{
		summary: summary{Mode: modeTraining, Scenario: "manual"},
		Source:  sourceState{Cursor: "0", Complete: -1},
		ActivePlan: plan{Operations: []Scheduled{
			{OperationID: "future", Start: 3, End: 4},
		}},
		ManualEvents: []event{{
			eventInput: eventInput{Type: railClosure, Available: &tick, Parameters: parameters{"from_tick": 0, "until_tick": 1}},
			Source:     "manual",
			ID:         "test",
		}},
	}, Seen: map[string]string{}}
	store := &Store{} // No mock client or queue is configured.
	if err := store.advance(context.Background(), nil, &r, tick); err != nil {
		t.Fatal(err)
	}
	if r.Status != "ready" || r.Tick != tick || r.Source != (sourceState{Cursor: "0", Complete: tick}) {
		t.Fatalf("unexpected training clock: %+v, %+v", r.summary, r.Source)
	}
	if len(r.Events) != 1 || r.Events[0].Source != "manual" || r.Events[0].Status != "expired" || len(r.ManualEvents) != 0 {
		t.Fatalf("manual event was not revealed: %+v", r.Events)
	}
}

func TestEventInputRejectsAmbiguousIntegers(t *testing.T) {
	for _, raw := range []string{
		`{"parameters":{"from_tick":null,"until_tick":2}}`,
		`{"parameters":{"from_tick":0,"from_tick":1,"until_tick":2}}`,
		`{"parameters":{"from_tick":0.5,"until_tick":2}}`,
	} {
		var input eventInput
		if err := strictJSON([]byte(raw), &input); err == nil {
			t.Errorf("accepted ambiguous input %s", raw)
		}
	}
}

func TestLateClosureChecksOngoingMovement(t *testing.T) {
	raw := json.RawMessage(`{"run_id":"demo","source":"rail-mock","event_id":"bm","type":"rail_edge_closed","target_id":"BM",
		"available_tick":0,"reason":"Ремонт","parameters":{"from_tick":0,"until_tick":1}}`)
	for _, tc := range []struct{ state, want string }{{"in_progress", "applied"}, {"completed", "expired"}} {
		t.Run(tc.state, func(t *testing.T) {
			r := storedRun{run: run{summary: summary{ID: "demo", Tick: 1},
				Targets:    []target{{ID: "BM", Type: railClosure}},
				Operations: []operation{{ID: "P2-BM", Edge: "BM"}},
				Facts:      []fact{{Scheduled{"P2-BM", 0, 2}, tc.state}},
			}, Seen: map[string]string{}}
			r.reveal([]json.RawMessage{raw})
			if len(r.Events) != 1 || r.Events[0].Status != tc.want {
				t.Fatalf("received %+v, expected %s", r.Events, tc.want)
			}
		})
	}
}

func TestConflictingSchedulesAreBothFlagged(t *testing.T) {
	tick := 1
	r := storedRun{run: run{summary: summary{ID: "demo", Tick: tick},
		Targets: []target{{ID: "A", Type: vesselChange}},
		ManualEvents: []event{
			{eventInput: eventInput{Type: vesselChange, Target: "A", Available: &tick, Reason: "Первое окно",
				Parameters: parameters{"load_from_tick": 9, "load_until_tick": 11, "departure_tick": 11}}, RunID: "demo", ID: "a", Source: "manual"},
			{eventInput: eventInput{Type: vesselChange, Target: "A", Available: &tick, Reason: "Второе окно",
				Parameters: parameters{"load_from_tick": 10, "load_until_tick": 12, "departure_tick": 12}}, RunID: "demo", ID: "b", Source: "manual"},
		},
	}, Seen: map[string]string{}}
	changed, conflict := r.reveal(nil)
	if changed || !conflict || len(r.Events) != 2 || r.Events[0].Status != "conflict" || r.Events[1].Status != "conflict" {
		t.Fatalf("expected two conflicting schedules and no revision: changed %v, conflict %v, events %+v", changed, conflict, r.Events)
	}
}

type pausedComputer struct {
	started    chan struct{}
	release    chan struct{}
	snapshotID string
}

func (*pausedComputer) Snapshot(context.Context, string) (calculations.Snapshot, error) {
	return calculations.Snapshot{}, errSeed
}

func (s *pausedComputer) Solve(ctx context.Context, req calculations.ComputeRequest) (calculations.ComputeResponse, error) {
	close(s.started)
	select {
	case <-ctx.Done():
		return calculations.ComputeResponse{}, ctx.Err()
	case <-s.release:
	}
	hash, _ := jsonutil.SHA256(req.Payload)
	raw, _ := json.Marshal(result{Status: "UNKNOWN", EngineProfile: profile, SnapshotID: s.snapshotID,
		CalculationID: req.RequestID, AttemptID: req.RequestID, InputSHA256: hash})
	return calculations.ComputeResponse{RequestID: req.RequestID, EngineProfile: profile, InputSHA256: hash, Result: raw, ValidationErrors: []string{}}, nil
}

func TestPostgresLateResultStaysInHistory(t *testing.T) {
	url := os.Getenv("RZD_SIMULATION_TEST_DB_URL")
	if url == "" {
		t.Skip("set RZD_SIMULATION_TEST_DB_URL to a separate rzd_arctica_simulation_worker_* database")
	}
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil || cfg.ConnConfig.Host != "127.0.0.1" || len(cfg.ConnConfig.Fallbacks) != 0 ||
		!strings.HasPrefix(cfg.ConnConfig.Database, "rzd_arctica_simulation_worker_") {
		t.Fatal("an isolated loopback simulation worker database is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	namespace := "sim_worker_" + strings.ReplaceAll(newID(), "-", "")
	quoted := pgx.Identifier{namespace}.Sanitize()
	if _, err = pool.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		t.Fatal(err)
	}
	defer pool.Exec(context.Background(), "DROP SCHEMA "+quoted+" CASCADE")
	cfg.ConnConfig.RuntimeParams["search_path"] = namespace
	isolated, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer isolated.Close()
	compute := &pausedComputer{started: make(chan struct{}), release: make(chan struct{}), snapshotID: newID()}
	store := New(isolated, nil, compute, "http://127.0.0.1:8094")
	if err = store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	runID, firstID, secondID := newID(), newID(), newID()
	r := storedRun{run: run{summary: summary{ID: runID, PlanVersion: 1, Tick: 1, Status: "queued"},
		ConditionRevision: 1, StateVersion: 2}, AttemptID: firstID}
	state, _ := json.Marshal(r)
	if _, err = isolated.Exec(ctx, "INSERT INTO rzd_sim_runs(id,state,created_by) VALUES($1,$2,'test')", runID, state); err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(map[string]string{"snapshot_id": compute.snapshotID})
	hash, _ := jsonutil.SHA256(json.RawMessage(payload))
	if _, err = isolated.Exec(ctx, `INSERT INTO rzd_sim_attempts(id,run_id,condition_revision,state_version,plan_version,input_sha256,payload,status)
		VALUES($1,$2,1,2,1,$3,$4,'queued')`, firstID, runID, hash, payload); err != nil {
		t.Fatal(err)
	}
	finished := make(chan error, 1)
	go func() {
		finished <- NewWorker(store).Work(ctx, &river.Job[JobArgs]{Args: JobArgs{RunID: runID, AttemptID: firstID}})
	}()
	select {
	case <-compute.started:
	case err := <-finished:
		t.Fatalf("worker exited before calculation: %v", err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	// Fault injection represents a replacement context before the previous HTTP reply.
	if _, err = store.update(ctx, runID, func(_ pgx.Tx, active *storedRun) error {
		active.StateVersion++
		active.AttemptID, active.Status, active.Message = secondID, "queued", "новая попытка"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	close(compute.release)
	if err = <-finished; err != nil {
		t.Fatal(err)
	}
	var status string
	var saved []byte
	if err = isolated.QueryRow(ctx, "SELECT status,result FROM rzd_sim_attempts WHERE id=$1", firstID).Scan(&status, &saved); err != nil {
		t.Fatal(err)
	}
	active, err := get(ctx, isolated, runID, false)
	if err != nil {
		t.Fatal(err)
	}
	if status != "stale" || len(saved) == 0 || active.AttemptID != secondID || active.Status != "queued" || active.Candidate != nil || active.Message != "новая попытка" {
		t.Fatalf("late result changed current run: attempt status %s, run status %s, message %s", status, active.Status, active.Message)
	}
}
