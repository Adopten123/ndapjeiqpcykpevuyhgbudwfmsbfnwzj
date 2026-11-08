package approvals

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"rzdarctica.local/backend/internal/features/calculations"
)

// A hypothetical envelope tests projection/binding only. It is deliberately
// not presented as a CP-SAT run or an independent optimum certificate.
func acceptedCalculationExample(t *testing.T) calculations.Calculation {
	t.Helper()
	c := projectionFixtureOnly(t, false)
	result, err := object(c.Result)
	if err != nil {
		t.Fatal(err)
	}
	result["group_decisions"] = []any{
		map[string]any{"group_id": "RAIL-P3", "party_id": "P3", "route_id": "P3-main", "service_id": "B", "departure_tick": 6},
		map[string]any{"group_id": "RAIL-P4", "party_id": "P4", "route_id": "P4-main", "service_id": "B", "departure_tick": 7},
	}
	c.Result, _ = json.Marshal(result)
	return c
}

func TestBaselineProjectsStoredCandidateAndDerivesAssignments(t *testing.T) {
	c := acceptedCalculationExample(t)
	baseline, err := baselineFromCalculation(c, "accepted-plan-17")
	if err != nil {
		t.Fatal(err)
	}
	value, err := object(baseline)
	if err != nil {
		t.Fatal(err)
	}
	result, _ := object(c.Result)
	a, _ := hashJSON(value["plan"])
	b, _ := hashJSON(result["candidate"])
	if value["plan_id"] != "accepted-plan-17" || a != b {
		t.Fatal("accepted candidate changed or mislabeled")
	}
	a, _ = hashJSON(value["assignments"])
	b, _ = hashJSON(result["group_decisions"])
	if a != b {
		t.Fatal("departure choices differ from the accepted plan")
	}
	for _, mode := range []string{"missing-decisions", "wrong-departure", "wrong-route", "duplicate-departure", "missing-party"} {
		t.Run(mode, func(t *testing.T) {
			c := acceptedCalculationExample(t)
			result, _ := object(c.Result)
			switch mode {
			case "missing-decisions":
				delete(result, "group_decisions")
			case "wrong-departure":
				result["group_decisions"].([]any)[0].(map[string]any)["departure_tick"] = 8
			case "wrong-route":
				result["group_decisions"].([]any)[0].(map[string]any)["route_id"] = "P3-alt"
			case "duplicate-departure":
				candidate := result["candidate"].(map[string]any)
				candidate["scheduled_operations"] = append(candidate["scheduled_operations"].([]any), map[string]any{"operation_id": "P3-OB", "start_tick": 6, "end_tick": 8})
			case "missing-party":
				candidate := result["candidate"].(map[string]any)
				candidate["party_plans"] = candidate["party_plans"].([]any)[:3]
			}
			c.Result, _ = json.Marshal(result)
			if _, err := baselineFromCalculation(c, "accepted-plan-17"); err == nil {
				t.Fatal("tampered accepted state became a new baseline")
			}
		})
	}
}

func TestSourceMutationChangesOnlyServiceAAndItsCommitment(t *testing.T) {
	c := acceptedCalculationExample(t)
	before, _ := object(c.Payload)
	changed, err := changeSource(c.Payload, 11, TickWindow{Start: 9, End: 11})
	if err != nil {
		t.Fatal(err)
	}
	after, _ := object(changed)
	for key, value := range before {
		if key == "services" || key == "commitments" {
			continue
		}
		a, _ := hashJSON(value)
		b, _ := hashJSON(after[key])
		if a != b {
			t.Fatal("unexpected source mutation", key)
		}
	}
	departure, window, err := sourceFields(changed)
	if err != nil || departure != 11 || window != (TickWindow{Start: 9, End: 11}) {
		t.Fatal("service update missing")
	}
	for _, key := range []string{"services", "commitments"} {
		a := before[key].([]any)
		b := after[key].([]any)
		if len(a) != len(b) {
			t.Fatal("member lost")
		}
		for i, raw := range b {
			row := raw.(map[string]any)
			selected := row["id"] == "A" || row["service_id"] == "A"
			if selected {
				tick, ok := integer(row["departure_tick"])
				if !ok || tick != 11 {
					t.Fatal("service and commitment disagree")
				}
			} else {
				ha, _ := hashJSON(a[i])
				hb, _ := hashJSON(b[i])
				if ha != hb {
					t.Fatal("unrelated service/commitment changed")
				}
			}
		}
	}
	// Capturing/updating must not mutate a caller's saved JSON.
	original, _ := hashJSON(c.Payload)
	again, _ := hashJSON(before)
	if original != again {
		t.Fatal("source mutated in place")
	}
	for _, window := range []TickWindow{{Start: -1, End: 8}, {Start: 8, End: 8}, {Start: 9, End: 8}, {Start: 8, End: 12}} {
		if _, err := changeSource(c.Payload, 11, window); err == nil {
			t.Fatal("invalid window accepted", window)
		}
	}
	if _, err := changeSource(c.Payload, 31, TickWindow{Start: 29, End: 30}); err == nil {
		t.Fatal("horizon extended")
	}
}

func TestCaptureHasUniqueIDsAndExactAcceptedBaseline(t *testing.T) {
	c := acceptedCalculationExample(t)
	baseline, err := baselineFromCalculation(c, "accepted-plan-17")
	if err != nil {
		t.Fatal(err)
	}
	h, _ := hashJSON(c.Payload)
	source := storedSource{Revision: 7, Hash: h, Payload: c.Payload}
	snapshot := "00000000-0000-4000-8000-000000000001"
	attempt := "00000000-0000-4000-8000-000000000002"
	id := "00000000-0000-4000-8000-000000000003"
	raw, err := capturePayload(source, baseline, id, snapshot, attempt)
	if err != nil {
		t.Fatal(err)
	}
	actual, _ := object(raw)
	original, _ := object(source.Payload)
	if actual["snapshot_id"] != snapshot || actual["calculation_id"] != id || actual["attempt_id"] != attempt {
		t.Fatal("capture reused template identifiers")
	}
	for key, value := range original {
		if key == "snapshot_id" || key == "calculation_id" || key == "attempt_id" || key == "baseline" {
			continue
		}
		a, _ := hashJSON(value)
		b, _ := hashJSON(actual[key])
		if a != b {
			t.Fatal("unexpected capture mutation", key)
		}
	}
	a, _ := hashJSON(actual["baseline"])
	b, _ := hashJSON(baseline)
	if a != b {
		t.Fatal("accepted baseline substituted")
	}
	unchanged, _ := hashJSON(source.Payload)
	if unchanged != source.Hash {
		t.Fatal("immutable source changed")
	}
}

func TestContextHashIncludesSourcesAndHeadButNotResponses(t *testing.T) {
	c := acceptedCalculationExample(t)
	baseline, err := baselineFromCalculation(c, "accepted-plan-17")
	if err != nil {
		t.Fatal(err)
	}
	id := "accepted-plan-17"
	ctx := Context{SourceRevision: 7, AcceptedVersion: 17, AcceptedPacketID: &id, ApprovalRevision: 10}
	h, _ := hashJSON(c.Payload)
	source := storedSource{Revision: 7, Hash: h, Payload: c.Payload}
	base, err := contextHash(ctx, source, baseline)
	if err != nil {
		t.Fatal(err)
	}
	responses := ctx
	responses.ApprovalRevision++
	same, _ := contextHash(responses, source, baseline)
	if same != base {
		t.Fatal("approval response invalidated immutable calculation")
	}
	for _, mode := range []string{"source-revision", "source-hash", "accepted-version", "accepted-id", "baseline-id", "baseline-assignment"} {
		t.Run(mode, func(t *testing.T) {
			current := ctx
			src := source
			bl := append(json.RawMessage(nil), baseline...)
			switch mode {
			case "source-revision":
				current.SourceRevision++
			case "source-hash":
				src.Hash = strings.Repeat("f", 64)
			case "accepted-version":
				current.AcceptedVersion++
			case "accepted-id":
				other := "accepted-plan-18"
				current.AcceptedPacketID = &other
			case "baseline-id":
				v, _ := object(bl)
				v["plan_id"] = "other"
				bl, _ = json.Marshal(v)
			case "baseline-assignment":
				v, _ := object(bl)
				v["assignments"].([]any)[0].(map[string]any)["departure_tick"] = 11
				bl, _ = json.Marshal(v)
			}
			different, err := contextHash(current, src, bl)
			if err != nil || different == base {
				t.Fatal("changed planning context retained its binding", err)
			}
		})
	}
}

func TestPlanningRequestsRejectMissingNullAndDuplicateNestedBounds(t *testing.T) {
	examples := []struct {
		body string
		ok   bool
	}{
		{`{"expected_source_revision":1,"expected_accepted_version":0,"service_a_departure_tick":11,"service_a_load_window":{"start_tick":9,"end_tick":11}}`, true},
		{`{"expected_source_revision":1,"service_a_departure_tick":11,"service_a_load_window":{"start_tick":9,"end_tick":11}}`, false},
		{`{"expected_source_revision":1,"expected_accepted_version":null,"service_a_departure_tick":11,"service_a_load_window":{"start_tick":9,"end_tick":11}}`, false},
		{`{"expected_source_revision":1,"expected_accepted_version":0,"service_a_departure_tick":11,"service_a_load_window":{"start_tick":9,"start_tick":8,"end_tick":11}}`, false},
		{`{"expected_source_revision":1,"expected_accepted_version":0,"service_a_departure_tick":11,"service_a_load_window":{"start_tick":null,"end_tick":11}}`, false},
		{`{"expected_source_revision":1,"expected_accepted_version":0,"service_a_departure_tick":11.5,"service_a_load_window":{"start_tick":9,"end_tick":11}}`, false},
		{`{"expected_source_revision":1,"expected_accepted_version":0,"service_a_departure_tick":11,"service_a_load_window":{"start_tick":9,"end_tick":11,"override":true}}`, false},
	}
	for _, example := range examples {
		req := httptest.NewRequest("POST", "/api/v1/planning-context/source", bytes.NewBufferString(example.body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", "source-test")
		w := httptest.NewRecorder()
		var value SourceRequest
		if ok := parse(w, req, &value); ok != example.ok {
			t.Fatal("request strictness", example.body, ok)
		}
	}
	var value PlanningRequest
	if requiredRequestFields([]byte(`{"expected_source_revision":1}`), &value) {
		t.Fatal("missing base accepted version silently became zero")
	}
}
