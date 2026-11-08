package approvals

import (
	"encoding/json"
	"fmt"
	"strings"

	"rzdarctica.local/backend/internal/core/jsonutil"
	"rzdarctica.local/backend/internal/features/calculations"
	calcservice "rzdarctica.local/backend/internal/features/calculations/service"
)

func hashJSON(value any) (string, error) {
	return jsonutil.SHA256(value)
}

type plannedParty struct {
	PartyID          string `json:"party_id"`
	RouteID          string `json:"route_id"`
	ServiceID        string `json:"service_id"`
	PortReceipt      int    `json:"port_receipt_tick"`
	RecipientReceipt int    `json:"recipient_receipt_tick"`
}
type operation struct {
	ID               string   `json:"id"`
	PartyID          string   `json:"party_id"`
	Kind             string   `json:"kind"`
	From             string   `json:"from_location_id"`
	To               string   `json:"to_location_id"`
	EdgeID           *string  `json:"edge_id"`
	RouteIDs         []string `json:"route_candidate_ids"`
	ServiceID        *string  `json:"service_id"`
	TransportGroupID *string  `json:"transport_group_id"`
	State            string   `json:"state"`
}
type operationDetails struct {
	ID      string  `json:"operation_id"`
	PartyID string  `json:"party_id"`
	Kind    string  `json:"kind"`
	From    string  `json:"from_location_id"`
	To      string  `json:"to_location_id"`
	EdgeID  *string `json:"edge_id"`
	Start   int     `json:"start_tick"`
	End     int     `json:"end_tick"`
	State   string  `json:"state"`
}
type endpoint struct {
	Kind        string `json:"kind"`
	OperationID string `json:"operation_id"`
	Tick        int    `json:"tick"`
}
type reservation struct {
	ID         string   `json:"id"`
	PartyID    string   `json:"party_id"`
	ResourceID string   `json:"resource_id"`
	RouteIDs   []string `json:"route_candidate_ids"`
	Start      endpoint `json:"start"`
	End        endpoint `json:"end"`
	ReleaseLag int      `json:"release_lag_tick"`
	Amount     int      `json:"amount"`
}
type reservationDetails struct {
	ID         string `json:"reservation_id"`
	ResourceID string `json:"resource_id"`
	PartyID    string `json:"party_id"`
	Start      int    `json:"start_tick"`
	End        int    `json:"end_tick"`
	ReleaseLag int    `json:"release_lag_tick"`
	Amount     int    `json:"amount"`
	Empty      bool   `json:"empty_interval"`
}

// BuildPacket is deliberately limited to the already validated synthetic S1.
// Routes, operations and occupancy are derived from this calculation, not a fixture.
func BuildPacket(c calculations.Calculation) (Packet, error) {
	var out Packet
	if c.ScenarioID != "S1" || c.Status != "succeeded" || c.EngineProfile != calculations.Profile && c.EngineProfile != calculations.WorkflowProfile {
		return out, ErrUnsupported
	}
	if err := calcservice.ValidateResponse(c, calculations.ComputeResponse{RequestID: c.ID, EngineProfile: c.EngineProfile, InputSHA256: c.InputSHA256, Result: c.Result, ValidationErrors: []string{}}); err != nil {
		return out, ErrUnsupported
	}
	inputHash, err := hashJSON(c.Payload)
	if err != nil || inputHash != c.InputSHA256 {
		return out, ErrUnsupported
	}
	var in struct {
		Mode               string            `json:"mode"`
		Operations         []operation       `json:"operations"`
		Reservations       []reservation     `json:"reservations"`
		Services           []json.RawMessage `json:"services"`
		Resources          []json.RawMessage `json:"resources"`
		ReceiptConstraints []json.RawMessage `json:"receipt_constraints"`
		Facts              json.RawMessage   `json:"facts"`
		Commitments        json.RawMessage   `json:"commitments"`
		Baseline           struct {
			PlanID string `json:"plan_id"`
		} `json:"baseline"`
		Policy struct {
			ID string `json:"id"`
		} `json:"policy"`
	}
	var result struct {
		SolverStatus string `json:"solver_status"`
		Candidate    struct {
			Plans     []plannedParty `json:"party_plans"`
			Scheduled []struct {
				ID    string `json:"operation_id"`
				Start int    `json:"start_tick"`
				End   int    `json:"end_tick"`
			} `json:"scheduled_operations"`
		} `json:"candidate"`
		CandidateRaw json.RawMessage `json:"-"`
	}
	if json.Unmarshal(c.Payload, &in) != nil || json.Unmarshal(c.Result, &result) != nil || result.SolverStatus != "OPTIMAL" || in.Mode != "replan" {
		return out, ErrUnsupported
	}
	var resultRaw map[string]json.RawMessage
	if json.Unmarshal(c.Result, &resultRaw) != nil {
		return out, ErrUnsupported
	}
	candidateHash, err := hashJSON(resultRaw["candidate"])
	if err != nil {
		return out, err
	}
	selected := map[string]bool{}
	plans := map[string]plannedParty{}
	for _, p := range result.Candidate.Plans {
		selected[p.RouteID] = true
		plans[p.PartyID] = p
	}
	active := func(ids []string) bool {
		for _, id := range ids {
			if selected[id] {
				return true
			}
		}
		return false
	}
	scheduled := map[string]operationDetails{}
	definitions := map[string]operation{}
	for _, op := range in.Operations {
		definitions[op.ID] = op
	}
	for _, t := range result.Candidate.Scheduled {
		op, ok := definitions[t.ID]
		if !ok || !active(op.RouteIDs) || t.End < t.Start {
			return out, ErrUnsupported
		}
		if _, exists := scheduled[t.ID]; exists {
			return out, ErrUnsupported
		}
		scheduled[t.ID] = operationDetails{ID: t.ID, PartyID: op.PartyID, Kind: op.Kind, From: op.From, To: op.To, EdgeID: op.EdgeID, Start: t.Start, End: t.End, State: op.State}
	}
	for _, op := range in.Operations {
		if active(op.RouteIDs) {
			if _, ok := scheduled[op.ID]; !ok {
				return out, ErrUnsupported
			}
		}
	}
	resolve := func(e endpoint) (int, error) {
		if e.Kind == "fixed_tick" {
			return e.Tick, nil
		}
		t, ok := scheduled[e.OperationID]
		if !ok {
			return 0, ErrUnsupported
		}
		switch e.Kind {
		case "operation_start":
			return t.Start, nil
		case "operation_end":
			return t.End, nil
		}
		return 0, ErrUnsupported
	}
	resolved := []reservationDetails{}
	for _, r := range in.Reservations {
		if !active(r.RouteIDs) {
			continue
		}
		a, e := resolve(r.Start)
		if e != nil {
			return out, e
		}
		b, e := resolve(r.End)
		if e != nil {
			return out, e
		}
		b += r.ReleaseLag
		if b < a || r.Amount < 1 || r.ReleaseLag < 0 {
			return out, ErrUnsupported
		}
		resolved = append(resolved, reservationDetails{ID: r.ID, ResourceID: r.ResourceID, PartyID: r.PartyID, Start: a, End: b, ReleaseLag: r.ReleaseLag, Amount: r.Amount, Empty: a == b})
	}
	resources := map[string]json.RawMessage{}
	for _, raw := range in.Resources {
		var r struct {
			ID string `json:"id"`
		}
		if json.Unmarshal(raw, &r) != nil {
			return out, ErrUnsupported
		}
		resources[r.ID] = raw
	}
	type spec struct {
		id, title, org string
		operation      func(operation) bool
		reserve        func(reservationDetails) bool
		party          string
		service        string
	}
	stringValue := func(s *string) string {
		if s == nil {
			return ""
		}
		return *s
	}
	specs := []spec{
		{id: "SERVICE-A", title: "Учебный рейс A", org: "SEA", service: "A"},
		{id: "SERVICE-B", title: "Учебный рейс B", org: "SEA", service: "B"},
		{id: "WAIT-O", title: "Ожидание на станции O", org: "RAIL", operation: func(o operation) bool { return o.Kind == "wait" && o.From == "O" }, reserve: func(r reservationDetails) bool { return r.ResourceID == "O-WAIT" }},
		{id: "RAIL-P3", title: "Железнодорожная подача P3", org: "RAIL", operation: func(o operation) bool { return stringValue(o.TransportGroupID) == "RAIL-P3" }, reserve: func(r reservationDetails) bool { return r.PartyID == "P3" && strings.HasPrefix(r.ResourceID, "RAIL-") }},
		{id: "RAIL-P4", title: "Железнодорожная подача P4", org: "RAIL", operation: func(o operation) bool { return stringValue(o.TransportGroupID) == "RAIL-P4" }, reserve: func(r reservationDetails) bool { return r.PartyID == "P4" && strings.HasPrefix(r.ResourceID, "RAIL-") }},
		{id: "PORT-M", title: "Хранение и погрузка в Мурманске", org: "PORT-M", operation: func(o operation) bool {
			return o.From == "M" && (o.Kind == "load" || o.Kind == "wait" && o.ServiceID == nil)
		}, reserve: func(r reservationDetails) bool { return r.ResourceID == "M-YARD" || r.ResourceID == "M-CRANE" }},
		{id: "PORT-D", title: "Разгрузка в порту назначения", org: "PORT-D", operation: func(o operation) bool { return o.Kind == "unload" && o.From == "D" }, reserve: func(r reservationDetails) bool { return r.ResourceID == "D-UNLOAD" }},
		{id: "LAST-MILE", title: "Доставка от порта получателям", org: "LAST-MILE", operation: func(o operation) bool { return o.From == "D" && o.To == "R" }, reserve: func(r reservationDetails) bool { return r.ResourceID == "LAST-MILE" }},
		{id: "ACCESS", title: "Будущий выпуск у отправителей", org: "ACCESS", operation: func(o operation) bool { return o.From == "S" && o.To == "O" }, reserve: func(r reservationDetails) bool { return r.ResourceID == "ACCESS" }},
	}
	for i := 1; i <= 4; i++ {
		id := fmt.Sprintf("P%d", i)
		specs = append(specs, spec{id: "RECIPIENT-" + id, title: "Получение партии " + id, org: "RECIPIENT-" + id, party: id})
	}
	for _, sp := range specs {
		detail := map[string]any{}
		ops := []operationDetails{}
		rs := []reservationDetails{}
		rr := []json.RawMessage{}
		ps := []plannedParty{}
		sv := []json.RawMessage{}
		rc := []json.RawMessage{}
		for _, t := range result.Candidate.Scheduled {
			o := definitions[t.ID]
			if sp.operation != nil && sp.operation(o) || sp.service != "" && stringValue(o.ServiceID) == sp.service && (o.Kind == "move" || o.Kind == "wait") {
				ops = append(ops, scheduled[t.ID])
			}
		}
		seenResources := map[string]bool{}
		for _, r := range resolved {
			if sp.reserve != nil && sp.reserve(r) {
				rs = append(rs, r)
				if !seenResources[r.ResourceID] {
					raw, ok := resources[r.ResourceID]
					if !ok {
						return out, ErrUnsupported
					}
					rr = append(rr, raw)
					seenResources[r.ResourceID] = true
				}
			}
		}
		for _, p := range result.Candidate.Plans {
			if sp.party == p.PartyID || sp.service != "" && sp.service == p.ServiceID {
				ps = append(ps, p)
			}
		}
		if sp.service != "" {
			for _, raw := range in.Services {
				var v struct {
					ID string `json:"id"`
				}
				if json.Unmarshal(raw, &v) != nil {
					return out, ErrUnsupported
				}
				if v.ID == sp.service {
					sv = append(sv, raw)
				}
			}
			if len(sv) != 1 {
				return out, ErrUnsupported
			}
		}
		if sp.party != "" {
			for _, raw := range in.ReceiptConstraints {
				var v struct {
					Party string `json:"party_id"`
				}
				if json.Unmarshal(raw, &v) != nil {
					return out, ErrUnsupported
				}
				if v.Party == sp.party {
					rc = append(rc, raw)
				}
			}
			if len(rc) != 1 || len(ps) != 1 {
				return out, ErrUnsupported
			}
		}
		if sp.operation != nil && len(ops) == 0 {
			return out, ErrUnsupported
		}
		detail["operations"] = ops
		detail["reservations"] = rs
		detail["resources"] = rr
		detail["party_plans"] = ps
		detail["services"] = sv
		detail["receipt_constraints"] = rc
		h, err := hashJSON(map[string]any{"id": sp.id, "organization_id": sp.org, "details": detail})
		if err != nil {
			return out, err
		}
		out.Subjects = append(out.Subjects, Subject{ID: sp.id, Title: sp.title, OrganizationID: sp.org, SHA256: h, Details: detail})
	}
	out.CalculationID = c.ID
	out.InputSHA256 = c.InputSHA256
	out.CandidateSHA256 = candidateHash
	out.SnapshotID = c.SnapshotID
	out.PolicyID = PolicyID
	out.Binding = map[string]any{"workflow_id": WorkflowID, "mode": "replan", "baseline_plan_id": in.Baseline.PlanID, "planning_policy_id": in.Policy.ID, "approval_policy_id": PolicyID, "calculation_id": c.ID, "input_sha256": c.InputSHA256, "candidate_sha256": candidateHash, "snapshot_id": c.SnapshotID, "source_revision": c.SourceRevision, "source_sha256": c.SourceSHA256, "base_accepted_version": c.BaseAcceptedVersion, "base_accepted_packet_id": c.BaseAcceptedPacketID, "baseline_sha256": c.BaselineSHA256, "context_sha256": c.ContextSHA256, "engine_profile": c.EngineProfile}
	out.FixedContext, _ = json.Marshal(map[string]any{"facts": in.Facts, "commitments": in.Commitments, "note": "Факты сохранены; COMMIT-A/B — учебные предпосылки, не подтверждения сторон"})
	out.Status = "pending"
	out.Issues = []string{}
	return out, nil
}
