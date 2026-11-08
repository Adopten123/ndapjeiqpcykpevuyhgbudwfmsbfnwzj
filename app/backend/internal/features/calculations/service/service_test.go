package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"rzdarctica.local/backend/internal/features/calculations"
)

type memoryStore struct {
	existing      calculations.Calculation
	existingError error
	creates       int
}

func (s *memoryStore) ByKey(context.Context, string) (calculations.Calculation, error) {
	return s.existing, s.existingError
}
func (s *memoryStore) CreateQueued(_ context.Context, id, key string, p calculations.Snapshot) (calculations.Calculation, error) {
	s.creates++
	return calculations.Calculation{ID: id, ScenarioID: p.ScenarioID, Status: "queued"}, nil
}
func (*memoryStore) Get(context.Context, string) (calculations.Calculation, error) {
	return calculations.Calculation{}, calculations.ErrNotFound
}
func (*memoryStore) List(context.Context) ([]calculations.Calculation, error) {
	return []calculations.Calculation{}, nil
}

type catalog struct {
	calls int
	err   error
}

func (c *catalog) Scenarios(context.Context) ([]calculations.Scenario, error) { return nil, c.err }
func (c *catalog) Snapshot(_ context.Context, id string) (calculations.Snapshot, error) {
	c.calls++
	return calculations.Snapshot{ScenarioID: id}, c.err
}

func TestIdempotentReplayDoesNotNeedCompute(t *testing.T) {
	s := &memoryStore{existing: calculations.Calculation{ID: "existing", ScenarioID: "S1", Status: "succeeded"}}
	c := &catalog{err: calculations.ErrCompute}
	value, err := New(s, c).Create(context.Background(), "same-key", "S1")
	if err != nil || value.ID != "existing" || c.calls != 0 || s.creates != 0 {
		t.Fatalf("replay was not preserved: %v", err)
	}
	if _, err = New(s, c).Create(context.Background(), "same-key", "S3"); !errors.Is(err, calculations.ErrConflict) {
		t.Fatalf("expected conflict, got %v", err)
	}
}

func TestNewRequestNeedsSnapshotBeforeStorage(t *testing.T) {
	s := &memoryStore{existingError: calculations.ErrNotFound}
	c := &catalog{err: calculations.ErrCompute}
	if _, err := New(s, c).Create(context.Background(), "new-key", "S1"); !errors.Is(err, calculations.ErrCompute) || s.creates != 0 {
		t.Fatal("stored a missing snapshot")
	}
	c.err = nil
	value, err := New(s, c).Create(context.Background(), "new-key", "S1")
	if err != nil || value.Status != "queued" || len(value.ID) != 36 || value.ID[14] != '4' || s.creates != 1 {
		t.Fatalf("unexpected create: %#v %v", value, err)
	}
}

func validBoundResult() (calculations.Calculation, calculations.ComputeResponse) {
	payload := json.RawMessage(`{"snapshot_id":"SNAP","calculation_id":"LEGACY","attempt_id":"A1","mode":"replan","data_mode":"demo","time_basis":{"now_tick":0,"horizon_end_tick":30},"parties":[{"id":"P1","allowed_service_ids":["A"]},{"id":"P2","allowed_service_ids":["A"]},{"id":"P3","allowed_service_ids":["B"]},{"id":"P4","allowed_service_ids":["B"]}],"route_candidates":[{"id":"P1-main","party_id":"P1"},{"id":"P2-main","party_id":"P2"},{"id":"P3-main","party_id":"P3"},{"id":"P4-main","party_id":"P4"}]}`)
	c := calculations.Calculation{ID: "a-app-id", ScenarioID: "S1", EngineProfile: calculations.Profile, SnapshotID: "SNAP", InputSHA256: strings.Repeat("a", 64), Payload: payload}
	r := calculations.ComputeResponse{RequestID: c.ID, EngineProfile: c.EngineProfile, InputSHA256: c.InputSHA256, ValidationErrors: []string{}}
	r.Result = json.RawMessage(`{"engine_profile":"research-demo.v1","execution":"executed","synthetic":true,"input_sha256":"` + c.InputSHA256 + `","snapshot_id":"SNAP","calculation_id":"LEGACY","attempt_id":"A1","mode":"replan","solver_status":"OPTIMAL","proven_prefix":3,"objective":{"sum_positive_shift":8,"max_positive_shift":4,"changed_groups":2},"optimization_stages":[{"objective_name":"sum_positive_shift","status":"OPTIMAL"},{"objective_name":"max_positive_shift","status":"OPTIMAL"},{"objective_name":"changed_groups","status":"OPTIMAL"}],"candidate":{"party_plans":[{"party_id":"P1","route_id":"P1-main","service_id":"A","port_receipt_tick":19,"recipient_receipt_tick":21},{"party_id":"P2","route_id":"P2-main","service_id":"A","port_receipt_tick":19,"recipient_receipt_tick":21},{"party_id":"P3","route_id":"P3-main","service_id":"B","port_receipt_tick":23,"recipient_receipt_tick":25},{"party_id":"P4","route_id":"P4-main","service_id":"B","port_receipt_tick":23,"recipient_receipt_tick":25}]}}`)
	return c, r
}

func mutateResult(r *calculations.ComputeResponse, fn func(map[string]any)) {
	var body map[string]any
	if err := json.Unmarshal(r.Result, &body); err != nil {
		panic(err)
	}
	fn(body)
	value, err := json.Marshal(body)
	if err != nil {
		panic(err)
	}
	r.Result = value
}

func TestComputeResultIsBoundToImmutableInput(t *testing.T) {
	c, r := validBoundResult()
	if err := ValidateResponse(c, r); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		mutate func(*calculations.ComputeResponse)
	}{
		{"other-app-request", func(r *calculations.ComputeResponse) { r.RequestID = "other" }},
		{"other-profile", func(r *calculations.ComputeResponse) { r.EngineProfile = "future" }},
		{"other-hash", func(r *calculations.ComputeResponse) { r.InputSHA256 = strings.Repeat("b", 64) }},
		{"missing-validation", func(r *calculations.ComputeResponse) { r.ValidationErrors = nil }},
		{"validator-rejects", func(r *calculations.ComputeResponse) { r.ValidationErrors = []string{"physical violation"} }},
		{"legacy-attempt-changed", func(r *calculations.ComputeResponse) {
			r.Result = json.RawMessage(strings.Replace(string(r.Result), `"A1"`, `"A2"`, 1))
		}},
		{"synthetic-flag-missing", func(r *calculations.ComputeResponse) {
			r.Result = json.RawMessage(strings.Replace(string(r.Result), `"synthetic":true`, `"synthetic":false`, 1))
		}},
		{"hypothetical-execution", func(r *calculations.ComputeResponse) {
			r.Result = json.RawMessage(strings.Replace(string(r.Result), `"executed"`, `"not_executed"`, 1))
		}},
		{"unknown-status", func(r *calculations.ComputeResponse) {
			r.Result = json.RawMessage(strings.Replace(string(r.Result), `"OPTIMAL"`, `"SUCCESS"`, 1))
		}},
		{"missing-optimal-candidate", func(r *calculations.ComputeResponse) {
			mutateResult(r, func(body map[string]any) { body["candidate"] = nil })
		}},
		{"empty-candidate-does-not-poison-history", func(r *calculations.ComputeResponse) {
			mutateResult(r, func(body map[string]any) { body["candidate"] = map[string]any{} })
		}},
		{"wrong-mode", func(r *calculations.ComputeResponse) {
			mutateResult(r, func(body map[string]any) { body["mode"] = "initial" })
		}},
		{"unproven-optimum", func(r *calculations.ComputeResponse) {
			mutateResult(r, func(body map[string]any) { body["proven_prefix"] = 1 })
		}},
		{"fractional-receipt-is-not-rounded", func(r *calculations.ComputeResponse) {
			mutateResult(r, func(body map[string]any) {
				body["candidate"].(map[string]any)["party_plans"].([]any)[0].(map[string]any)["port_receipt_tick"] = 19.5
			})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, changed := validBoundResult()
			tc.mutate(&changed)
			if ValidateResponse(c, changed) == nil {
				t.Fatal("invalid result accepted")
			}
		})
	}
	mutateResult(&r, func(body map[string]any) {
		body["solver_status"], body["candidate"], body["objective"], body["proven_prefix"] = "INFEASIBLE", nil, nil, 0
		body["optimization_stages"] = []any{map[string]any{"objective_name": "sum_positive_shift", "status": "INFEASIBLE"}}
	})
	if err := ValidateResponse(c, r); err != nil {
		t.Fatal("infeasible is a valid completed calculation", err)
	}
	_, r = validBoundResult()
	mutateResult(&r, func(body map[string]any) {
		body["solver_status"], body["proven_prefix"] = "UNKNOWN", 1
		body["optimization_stages"] = []any{map[string]any{"objective_name": "sum_positive_shift", "status": "OPTIMAL"}}
	})
	if err := ValidateResponse(c, r); err != nil {
		t.Fatal("retained candidate and partial proof must not become infeasibility", err)
	}
}
