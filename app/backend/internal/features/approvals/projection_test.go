package approvals

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"rzdarctica.local/backend/internal/features/calculations"
)

// This fixture adapts a hypothetical candidate to the native envelope solely
// to test projection. It is not a solver run or a proof of the invented status.
func projectionFixtureOnly(t *testing.T, alternate bool) calculations.Calculation {
	t.Helper()
	root := filepath.Join("..", "..", "..", "..", "..")
	load := func(name string) map[string]any {
		data, err := os.ReadFile(filepath.Join(root, "app", "compute", "contracts", "planning-modes", "examples", name))
		if err != nil {
			t.Fatal(err)
		}
		var wrapper struct {
			Payload map[string]any `json:"payload"`
		}
		if json.Unmarshal(data, &wrapper) != nil {
			t.Fatal("fixture JSON")
		}
		return wrapper.Payload
	}
	input := load("replan-request.fixture.json")
	response := load("replan-response.fixture.json")
	candidate := response["candidate"].(map[string]any)
	scheduled := candidate["scheduled_operations"].([]any)
	scheduled = append(scheduled, map[string]any{"operation_id": "P2-OB-PAST", "start_tick": -2, "end_tick": 0})
	if alternate {
		for _, raw := range candidate["party_plans"].([]any) {
			p := raw.(map[string]any)
			if p["party_id"] == "P3" || p["party_id"] == "P4" {
				p["route_id"] = p["party_id"].(string) + "-alt"
			}
		}
		for _, raw := range scheduled {
			op := raw.(map[string]any)
			id := op["operation_id"].(string)
			if strings.HasPrefix(id, "P3-") || strings.HasPrefix(id, "P4-") {
				switch {
				case strings.HasSuffix(id, "WAIT-O"):
					op["operation_id"] = id + "-ALT"
				case strings.HasSuffix(id, "-OB"):
					op["operation_id"] = strings.TrimSuffix(id, "OB") + "OC"
				case strings.HasSuffix(id, "-BM"):
					op["operation_id"] = strings.TrimSuffix(id, "BM") + "CM"
				}
			}
		}
		// Explicit zero WAIT remains a zero occupancy interval in the projection.
		for _, raw := range scheduled {
			op := raw.(map[string]any)
			if op["operation_id"] == "P4-WAIT-O-ALT" {
				op["end_tick"] = op["start_tick"]
			}
		}
	}
	candidate["scheduled_operations"] = scheduled
	payload, _ := json.Marshal(input)
	hash, err := hashJSON(json.RawMessage(payload))
	if err != nil {
		t.Fatal(err)
	}
	result := map[string]any{"engine_profile": calculations.Profile, "execution": "executed", "synthetic": true, "mode": "replan", "solver_status": "OPTIMAL", "input_sha256": hash, "snapshot_id": input["snapshot_id"], "calculation_id": input["calculation_id"], "attempt_id": input["attempt_id"], "candidate": candidate, "proven_prefix": 3, "objective": response["objective"], "optimization_stages": []any{map[string]any{"objective_name": "sum_positive_shift", "status": "OPTIMAL"}, map[string]any{"objective_name": "max_positive_shift", "status": "OPTIMAL"}, map[string]any{"objective_name": "changed_groups", "status": "OPTIMAL"}}}
	raw, _ := json.Marshal(result)
	return calculations.Calculation{ID: "11111111-1111-4111-8111-111111111111", ScenarioID: "S1", Status: "succeeded", EngineProfile: calculations.Profile, SnapshotID: input["snapshot_id"].(string), InputSHA256: hash, Payload: payload, Result: raw}
}
func subjectByID(t *testing.T, p Packet, id string) Subject {
	t.Helper()
	for _, s := range p.Subjects {
		if s.ID == id {
			return s
		}
	}
	t.Fatal("missing subject", id)
	return Subject{}
}

func TestProjectionFollowsSelectedRouteAndReleaseLag(t *testing.T) {
	for _, alternate := range []bool{false, true} {
		c := projectionFixtureOnly(t, alternate)
		p, err := BuildPacket(c)
		if err != nil {
			t.Fatal(err)
		}
		if len(p.Subjects) != 13 || p.InputSHA256 != c.InputSHA256 || p.PolicyID != PolicyID {
			t.Fatal("binding/coverage")
		}
		rail := subjectByID(t, p, "RAIL-P3")
		ops := rail.Details["operations"].([]operationDetails)
		if len(ops) != 2 {
			t.Fatal("rail ops", ops)
		}
		wanted := "OB"
		if alternate {
			wanted = "OC"
		}
		if !strings.HasSuffix(ops[0].ID, wanted) {
			t.Fatalf("wrong route projection: %v", ops)
		}
		port := subjectByID(t, p, "PORT-M")
		reservations := port.Details["reservations"].([]reservationDetails)
		seen := false
		for _, r := range reservations {
			if r.ID == "YARD-P1-main" {
				seen = true
				if r.End != 10 || r.ReleaseLag != 1 {
					t.Fatal("release lag lost", r)
				}
			}
		}
		if !seen {
			t.Fatal("missing yard")
		}
		wait := subjectByID(t, p, "WAIT-O")
		rs := wait.Details["reservations"].([]reservationDetails)
		if alternate {
			empty := false
			for _, r := range rs {
				if r.PartyID == "P4" {
					empty = r.Empty && r.Start == r.End
				}
			}
			if !empty {
				t.Fatal("zero WAIT became occupancy")
			}
		}
		for _, s := range p.Subjects {
			for _, op := range s.Details["operations"].([]operationDetails) {
				if op.State != "planned" {
					t.Fatal("past/in-progress must be context, not permission", op.ID)
				}
			}
		}
		if p.Binding["baseline_plan_id"] != "DEMO-v0" {
			t.Fatal("baseline replaced")
		}
	}
}
func TestProjectionRejectsUnsupportedOrBrokenCandidate(t *testing.T) {
	for _, scenario := range []string{"S0", "S2", "S3"} {
		c := projectionFixtureOnly(t, false)
		c.ScenarioID = scenario
		if _, err := BuildPacket(c); err != ErrUnsupported {
			t.Fatal(scenario, err)
		}
	}
	for _, mode := range []string{"input-hash", "nonoptimal", "missing-operation", "duplicate-operation"} {
		t.Run(mode, func(t *testing.T) {
			c := projectionFixtureOnly(t, true)
			if mode == "input-hash" {
				c.InputSHA256 = strings.Repeat("0", 64)
			} else {
				var result map[string]any
				json.Unmarshal(c.Result, &result)
				switch mode {
				case "nonoptimal":
					result["solver_status"] = "FEASIBLE"
				case "missing-operation":
					candidate := result["candidate"].(map[string]any)
					candidate["scheduled_operations"] = candidate["scheduled_operations"].([]any)[1:]
				case "duplicate-operation":
					candidate := result["candidate"].(map[string]any)
					v := candidate["scheduled_operations"].([]any)
					candidate["scheduled_operations"] = append(v, v[0])
				}
				c.Result, _ = json.Marshal(result)
			}
			if _, err := BuildPacket(c); err != ErrUnsupported {
				t.Fatal("accepted broken candidate", err)
			}
		})
	}
}
func TestHashCanonicalJSON(t *testing.T) {
	a, _ := hashJSON(json.RawMessage(`{"b":2,"a":"<тест>"}`))
	b, _ := hashJSON(json.RawMessage("{\n\"a\":\"<тест>\",\"b\":2}"))
	if a != b {
		t.Fatal("object order/spacing changed hash")
	}
}
