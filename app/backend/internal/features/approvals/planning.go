package approvals

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"rzdarctica.local/backend/internal/features/calculations"
)

type TickWindow struct {
	Start int `json:"start_tick"`
	End   int `json:"end_tick"`
}
type PlanningRequest struct {
	ExpectedSourceRevision  int64 `json:"expected_source_revision"`
	ExpectedAcceptedVersion int64 `json:"expected_accepted_version"`
}
type SourceRequest struct {
	ExpectedSourceRevision  int64      `json:"expected_source_revision"`
	ExpectedAcceptedVersion int64      `json:"expected_accepted_version"`
	Departure               int        `json:"service_a_departure_tick"`
	LoadWindow              TickWindow `json:"service_a_load_window"`
}
type SourceView struct {
	Revision    int64      `json:"revision"`
	InputSHA256 string     `json:"input_sha256"`
	Departure   int        `json:"service_a_departure_tick"`
	LoadWindow  TickWindow `json:"service_a_load_window"`
}
type BaselineView struct {
	AcceptedVersion int64   `json:"accepted_version"`
	PacketID        *string `json:"packet_id"`
	PlanID          string  `json:"plan_id"`
	CandidateSHA256 string  `json:"candidate_sha256"`
	BaselineSHA256  string  `json:"baseline_sha256"`
}
type PlanningDocument struct {
	Context               Context      `json:"context"`
	Source                SourceView   `json:"source"`
	Baseline              BaselineView `json:"baseline"`
	CanCalculate          bool         `json:"can_calculate"`
	ContextSHA256         string       `json:"context_sha256"`
	AppliedSourceRevision *int64       `json:"applied_source_revision,omitempty"`
}
type storedSource struct {
	Revision int64
	Hash     string
	Payload  json.RawMessage
}

func object(raw []byte) (map[string]any, error) {
	var value map[string]any
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if err := d.Decode(&value); err != nil || value == nil {
		return nil, ErrInvalid
	}
	return value, nil
}
func integer(v any) (int, bool) {
	n, ok := v.(json.Number)
	if !ok {
		return 0, false
	}
	i, err := n.Int64()
	return int(i), err == nil && int64(int(i)) == i
}
func sourceFields(raw json.RawMessage) (int, TickWindow, error) {
	var p struct {
		Services []struct {
			ID        string     `json:"id"`
			Departure int        `json:"departure_tick"`
			Window    TickWindow `json:"load_window"`
		} `json:"services"`
	}
	if json.Unmarshal(raw, &p) != nil {
		return 0, TickWindow{}, ErrInvalid
	}
	for _, service := range p.Services {
		if service.ID == "A" {
			if !validWindow(service.Departure, service.Window) {
				return 0, TickWindow{}, ErrInvalid
			}
			return service.Departure, service.Window, nil
		}
	}
	return 0, TickWindow{}, ErrInvalid
}
func validWindow(departure int, window TickWindow) bool {
	return 0 <= window.Start && window.Start < window.End && window.End <= departure && departure <= 30
}

// changeSource copies the immutable source. It never changes a receipt window,
// baseline, graph, fact, or a resource to make the requested departure feasible.
func changeSource(raw json.RawMessage, departure int, window TickWindow) (json.RawMessage, error) {
	if !validWindow(departure, window) {
		return nil, ErrInvalid
	}
	p, err := object(raw)
	if err != nil {
		return nil, err
	}
	services, ok := p["services"].([]any)
	if !ok {
		return nil, ErrInvalid
	}
	count := 0
	for _, v := range services {
		item, ok := v.(map[string]any)
		if !ok {
			return nil, ErrInvalid
		}
		if item["id"] == "A" {
			count++
			item["departure_tick"] = departure
			item["load_window"] = window
		}
	}
	if count != 1 {
		return nil, ErrInvalid
	}
	commitments, ok := p["commitments"].([]any)
	if !ok {
		return nil, ErrInvalid
	}
	count = 0
	for _, v := range commitments {
		item, ok := v.(map[string]any)
		if !ok {
			return nil, ErrInvalid
		}
		if item["kind"] == "service_conditions" && item["service_id"] == "A" {
			count++
			item["departure_tick"] = departure
			item["load_window"] = window
		}
	}
	if count != 1 {
		return nil, ErrInvalid
	}
	return json.Marshal(p)
}

func readSource(ctx context.Context, tx pgx.Tx, revision int64) (storedSource, error) {
	var source storedSource
	source.Revision = revision
	err := tx.QueryRow(ctx, `SELECT input_sha256,payload FROM rzd_planning_sources WHERE workflow_id=$1 AND revision=$2`, WorkflowID, revision).Scan(&source.Hash, &source.Payload)
	if errors.Is(err, pgx.ErrNoRows) {
		return source, ErrSourceUnavailable
	}
	if err != nil {
		return source, err
	}
	h, err := hashJSON(source.Payload)
	if err != nil || h != source.Hash {
		return source, ErrConflict
	}
	return source, nil
}

// EnsureSource bootstraps exactly once. Existing revisions are never rebuilt
// from a newly fetched template. A legacy synthetic invalidation is not data.
func (s *Store) HasSource(ctx context.Context) (bool, error) {
	var exists bool
	err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM rzd_planning_sources WHERE workflow_id=$1)`, WorkflowID).Scan(&exists)
	return exists, err
}
func (s *Store) EnsureSource(ctx context.Context, snapshot calculations.Snapshot) error {
	if snapshot.ScenarioID != "S1" || snapshot.EngineProfile != calculations.Profile {
		return ErrInvalid
	}
	if _, _, err := sourceFields(snapshot.Payload); err != nil {
		return err
	}
	h, err := hashJSON(snapshot.Payload)
	if err != nil || h != snapshot.InputSHA256 {
		return ErrInvalid
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background())
	c, err := readContext(ctx, tx)
	if err != nil {
		return err
	}
	var exists bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM rzd_planning_sources WHERE workflow_id=$1)`, WorkflowID).Scan(&exists); err != nil {
		return err
	}
	if exists {
		return tx.Commit(ctx)
	}
	if c.SourceRevision != 1 {
		return ErrSourceUnavailable
	}
	_, err = tx.Exec(ctx, `INSERT INTO rzd_planning_sources(workflow_id,revision,input_sha256,payload) VALUES($1,1,$2,$3::jsonb)`, WorkflowID, h, string(snapshot.Payload))
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// acceptedBaseline projects the exact stored candidate. Group decisions are
// independently derived from selected departure operations before reuse.
func baselineFromCalculation(c calculations.Calculation, planID string) (json.RawMessage, error) {
	if _, err := BuildPacket(c); err != nil {
		return nil, err
	}
	in, err := object(c.Payload)
	if err != nil {
		return nil, err
	}
	out, err := object(c.Result)
	if err != nil {
		return nil, err
	}
	candidate, ok := out["candidate"].(map[string]any)
	if !ok {
		return nil, ErrConflict
	}
	plans, ok := candidate["party_plans"].([]any)
	if !ok || len(plans) != 4 {
		return nil, ErrConflict
	}
	byParty := map[string]map[string]any{}
	for _, raw := range plans {
		plan, ok := raw.(map[string]any)
		if !ok {
			return nil, ErrConflict
		}
		id, ok := plan["party_id"].(string)
		if !ok || byParty[id] != nil {
			return nil, ErrConflict
		}
		byParty[id] = plan
	}
	times := map[string]int{}
	ops, ok := candidate["scheduled_operations"].([]any)
	if !ok {
		return nil, ErrConflict
	}
	for _, raw := range ops {
		op, ok := raw.(map[string]any)
		if !ok {
			return nil, ErrConflict
		}
		id, ok := op["operation_id"].(string)
		tick, valid := integer(op["start_tick"])
		if !ok || !valid {
			return nil, ErrConflict
		}
		if _, exists := times[id]; exists {
			return nil, ErrConflict
		}
		times[id] = tick
	}
	groups, ok := in["controllable_groups"].([]any)
	if !ok || len(groups) != 2 {
		return nil, ErrConflict
	}
	assignments := []any{}
	for _, raw := range groups {
		group, ok := raw.(map[string]any)
		if !ok {
			return nil, ErrConflict
		}
		partyID, ok := group["party_id"].(string)
		if !ok {
			return nil, ErrConflict
		}
		plan := byParty[partyID]
		if plan == nil {
			return nil, ErrConflict
		}
		ids, ok := group["departure_operation_ids"].([]any)
		if !ok {
			return nil, ErrConflict
		}
		n, departure := 0, 0
		for _, rawID := range ids {
			id, ok := rawID.(string)
			if !ok {
				return nil, ErrConflict
			}
			if tick, found := times[id]; found {
				n++
				departure = tick
			}
		}
		if n != 1 {
			return nil, ErrConflict
		}
		assignments = append(assignments, map[string]any{"group_id": group["group_id"], "party_id": partyID, "route_id": plan["route_id"], "service_id": plan["service_id"], "departure_tick": departure})
	}
	a, err := hashJSON(assignments)
	if err != nil {
		return nil, err
	}
	b, err := hashJSON(out["group_decisions"])
	if err != nil || a != b {
		return nil, ErrConflict
	}
	return json.Marshal(map[string]any{"plan_id": planID, "plan": candidate, "assignments": assignments})
}
func baselineFor(ctx context.Context, tx pgx.Tx, c Context, source storedSource) (json.RawMessage, BaselineView, error) {
	view := BaselineView{AcceptedVersion: c.AcceptedVersion, PacketID: c.AcceptedPacketID}
	var baseline json.RawMessage
	if c.AcceptedVersion == 0 {
		if c.AcceptedPacketID != nil {
			return nil, view, ErrConflict
		}
		p, err := object(source.Payload)
		if err != nil {
			return nil, view, err
		}
		baseline, err = json.Marshal(p["baseline"])
		if err != nil {
			return nil, view, err
		}
	} else {
		if c.AcceptedPacketID == nil {
			return nil, view, ErrConflict
		}
		packet, err := packetByID(ctx, tx, *c.AcceptedPacketID)
		if err != nil {
			return nil, view, err
		}
		if packet.Acceptance == nil || packet.Acceptance.AcceptedVersion != c.AcceptedVersion {
			return nil, view, ErrConflict
		}
		calculation, err := calc(ctx, tx, packet.CalculationID)
		if err != nil {
			return nil, view, err
		}
		baseline, err = baselineFromCalculation(calculation, packet.ID)
		if err != nil {
			return nil, view, err
		}
		var result struct {
			Candidate json.RawMessage `json:"candidate"`
		}
		if json.Unmarshal(calculation.Result, &result) != nil {
			return nil, view, ErrConflict
		}
		h, err := hashJSON(result.Candidate)
		if err != nil || h != packet.CandidateSHA256 {
			return nil, view, ErrConflict
		}
	}
	var value struct {
		PlanID string          `json:"plan_id"`
		Plan   json.RawMessage `json:"plan"`
	}
	if json.Unmarshal(baseline, &value) != nil || value.PlanID == "" || len(value.Plan) == 0 {
		return nil, view, ErrConflict
	}
	view.PlanID = value.PlanID
	var err error
	view.CandidateSHA256, err = hashJSON(value.Plan)
	if err != nil {
		return nil, view, err
	}
	view.BaselineSHA256, err = hashJSON(baseline)
	return baseline, view, err
}
func contextHash(c Context, source storedSource, baseline json.RawMessage) (string, error) {
	var value struct {
		PlanID string          `json:"plan_id"`
		Plan   json.RawMessage `json:"plan"`
	}
	if json.Unmarshal(baseline, &value) != nil {
		return "", ErrConflict
	}
	baselineSHA, err := hashJSON(baseline)
	if err != nil {
		return "", err
	}
	candidateSHA, err := hashJSON(value.Plan)
	if err != nil {
		return "", err
	}
	var payload struct {
		Policy struct {
			ID string `json:"id"`
		} `json:"policy"`
	}
	if json.Unmarshal(source.Payload, &payload) != nil {
		return "", ErrConflict
	}
	return hashJSON(map[string]any{"workflow_id": WorkflowID, "engine_profile": calculations.WorkflowProfile, "source_revision": c.SourceRevision, "source_sha256": source.Hash, "base_accepted_version": c.AcceptedVersion, "base_accepted_packet_id": c.AcceptedPacketID, "baseline_sha256": baselineSHA, "candidate_sha256": candidateSHA, "baseline_plan_id": value.PlanID, "planning_policy_id": payload.Policy.ID, "approval_policy_id": PolicyID})
}
func planningDocument(ctx context.Context, tx pgx.Tx, c Context, g grant) (PlanningDocument, error) {
	source, err := readSource(ctx, tx, c.SourceRevision)
	if err != nil {
		return PlanningDocument{}, err
	}
	baselinePayload, baseline, err := baselineFor(ctx, tx, c, source)
	if err != nil {
		return PlanningDocument{}, err
	}
	contextSHA, err := contextHash(c, source, baselinePayload)
	if err != nil {
		return PlanningDocument{}, err
	}
	departure, window, err := sourceFields(source.Payload)
	if err != nil {
		return PlanningDocument{}, err
	}
	t, err := currentTime(ctx, tx)
	if err != nil {
		return PlanningDocument{}, err
	}
	if !validGrant(g, t) {
		return PlanningDocument{}, ErrForbidden
	}
	return PlanningDocument{Context: c, Source: SourceView{Revision: source.Revision, InputSHA256: source.Hash, Departure: departure, LoadWindow: window}, Baseline: baseline, ContextSHA256: contextSHA, CanCalculate: g.User.Role == "coordinator"}, nil
}
func (s *Store) PlanningContext(ctx context.Context, userID string) (PlanningDocument, error) {
	tx, c, g, err := s.begin(ctx, userID)
	if err != nil {
		return PlanningDocument{}, err
	}
	defer tx.Rollback(context.Background())
	if g.User.Role != "coordinator" && g.User.Role != "auditor" {
		return PlanningDocument{}, ErrForbidden
	}
	return planningDocument(ctx, tx, c, g)
}

func capturePayload(source storedSource, baseline json.RawMessage, id, snapshotID, attemptID string) (json.RawMessage, error) {
	p, err := object(source.Payload)
	if err != nil {
		return nil, err
	}
	var b any
	d := json.NewDecoder(bytes.NewReader(baseline))
	d.UseNumber()
	if d.Decode(&b) != nil {
		return nil, ErrConflict
	}
	p["baseline"] = b
	p["snapshot_id"] = snapshotID
	p["calculation_id"] = id
	p["attempt_id"] = attemptID
	return json.Marshal(p)
}
func sameID(a, b *string) bool { return a == nil && b == nil || a != nil && b != nil && *a == *b }

// validateBinding is called inside the workflow lock at packet creation and
// final acceptance. Wall-clock creation time is deliberately not its authority.
func validateBinding(ctx context.Context, tx pgx.Tx, c Context, calculation calculations.Calculation) error {
	source, err := readSource(ctx, tx, c.SourceRevision)
	if err != nil {
		return err
	}
	if calculation.EngineProfile == calculations.Profile {
		if c.AcceptedVersion != 0 || c.SourceRevision != 1 || calculation.SourceRevision != 0 || calculation.InputSHA256 != source.Hash {
			return ErrConflict
		}
		return nil
	}
	if calculation.EngineProfile != calculations.WorkflowProfile || calculation.SourceRevision != c.SourceRevision || calculation.BaseAcceptedVersion != c.AcceptedVersion || !sameID(calculation.BaseAcceptedPacketID, c.AcceptedPacketID) || calculation.SourceSHA256 != source.Hash {
		return ErrConflict
	}
	baseline, _, err := baselineFor(ctx, tx, c, source)
	if err != nil {
		return err
	}
	baselineSHA, err := hashJSON(baseline)
	if err != nil || baselineSHA != calculation.BaselineSHA256 {
		return ErrConflict
	}
	contextSHA, err := contextHash(c, source, baseline)
	if err != nil || contextSHA != calculation.ContextSHA256 {
		return ErrConflict
	}
	var p struct {
		Snapshot    string `json:"snapshot_id"`
		Calculation string `json:"calculation_id"`
		Attempt     string `json:"attempt_id"`
	}
	if json.Unmarshal(calculation.Payload, &p) != nil || p.Calculation != calculation.ID || p.Snapshot != calculation.SnapshotID || !uuidPattern.MatchString(p.Attempt) || !uuidPattern.MatchString(p.Snapshot) {
		return ErrConflict
	}
	expected, err := capturePayload(source, baseline, p.Calculation, p.Snapshot, p.Attempt)
	if err != nil {
		return err
	}
	h, err := hashJSON(expected)
	if err != nil || h != calculation.InputSHA256 {
		return ErrConflict
	}
	actual, err := hashJSON(calculation.Payload)
	if err != nil || actual != h {
		return ErrConflict
	}
	return nil
}

func planningReplay(ctx context.Context, tx pgx.Tx, actor, action, key, hash string) (*string, *int64, bool, error) {
	var old string
	var id *string
	var revision *int64
	err := tx.QueryRow(ctx, `SELECT body_sha256,calculation_id::text,source_revision FROM rzd_planning_idempotency WHERE actor_id=$1 AND action=$2 AND key=$3`, actor, action, key).Scan(&old, &id, &revision)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil, false, nil
	}
	if err != nil {
		return nil, nil, false, err
	}
	if old != hash {
		return nil, nil, false, ErrConflict
	}
	return id, revision, true, nil
}
func (s *Store) PlanningCalculation(ctx context.Context, userID, key string, req PlanningRequest) (calculations.Calculation, error) {
	tx, c, g, err := s.begin(ctx, userID)
	if err != nil {
		return calculations.Calculation{}, err
	}
	defer tx.Rollback(context.Background())
	if g.User.Role != "coordinator" {
		return calculations.Calculation{}, ErrForbidden
	}
	h, err := hashJSON(req)
	if err != nil {
		return calculations.Calculation{}, err
	}
	if id, _, found, err := planningReplay(ctx, tx, userID, "calculate", key, h); err != nil {
		return calculations.Calculation{}, err
	} else if found {
		if id == nil {
			return calculations.Calculation{}, ErrConflict
		}
		result, err := calc(ctx, tx, *id)
		if err != nil {
			return result, err
		}
		now, err := currentTime(ctx, tx)
		if err != nil {
			return result, err
		}
		if !validGrant(g, now) {
			return calculations.Calculation{}, ErrForbidden
		}
		return result, nil
	}
	if req.ExpectedSourceRevision != c.SourceRevision || req.ExpectedAcceptedVersion != c.AcceptedVersion {
		return calculations.Calculation{}, ErrConflict
	}
	source, err := readSource(ctx, tx, c.SourceRevision)
	if err != nil {
		return calculations.Calculation{}, err
	}
	baseline, _, err := baselineFor(ctx, tx, c, source)
	if err != nil {
		return calculations.Calculation{}, err
	}
	id, err := newID()
	if err != nil {
		return calculations.Calculation{}, err
	}
	snapshot, err := newID()
	if err != nil {
		return calculations.Calculation{}, err
	}
	attempt, err := newID()
	if err != nil {
		return calculations.Calculation{}, err
	}
	payload, err := capturePayload(source, baseline, id, snapshot, attempt)
	if err != nil {
		return calculations.Calculation{}, err
	}
	inputSHA, err := hashJSON(payload)
	if err != nil {
		return calculations.Calculation{}, err
	}
	baselineSHA, err := hashJSON(baseline)
	if err != nil {
		return calculations.Calculation{}, err
	}
	contextSHA, err := contextHash(c, source, baseline)
	if err != nil {
		return calculations.Calculation{}, err
	}
	t, err := currentTime(ctx, tx)
	if err != nil {
		return calculations.Calculation{}, err
	}
	if !validGrant(g, t) {
		return calculations.Calculation{}, ErrForbidden
	}
	internalKey, err := hashJSON(map[string]any{"actor_id": userID, "key": key, "action": "workflow-calculation"})
	if err != nil {
		return calculations.Calculation{}, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO rzd_calculations(id,idempotency_key,scenario_id,status,engine_profile,snapshot_id,input_sha256,input_payload,source_revision,base_accepted_version,base_accepted_packet_id,source_sha256,baseline_sha256,context_sha256,created_at,updated_at) VALUES($1::uuid,$2,'S1','queued',$3,$4,$5,$6::jsonb,$7,$8,$9::uuid,$10,$11,$12,$13,$13)`, id, "wf:"+internalKey, calculations.WorkflowProfile, snapshot, inputSHA, string(payload), c.SourceRevision, c.AcceptedVersion, c.AcceptedPacketID, source.Hash, baselineSHA, contextSHA, t)
	if err != nil {
		return calculations.Calculation{}, err
	}
	if s.queue == nil {
		return calculations.Calculation{}, errors.New("workflow queue unavailable")
	}
	if _, err = s.queue.InsertTx(ctx, tx, calculations.JobArgs{CalculationID: id}, &river.InsertOpts{Queue: calculations.Queue, MaxAttempts: 3}); err != nil {
		return calculations.Calculation{}, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO rzd_planning_idempotency(actor_id,action,key,body_sha256,calculation_id,source_revision) VALUES($1,'calculate',$2,$3,$4::uuid,$5)`, userID, key, h, id, c.SourceRevision); err != nil {
		return calculations.Calculation{}, err
	}
	if err = audit(ctx, tx, userID, "workflow_calculation_created", nil, map[string]any{"calculation_id": id, "source_revision": c.SourceRevision, "base_accepted_version": c.AcceptedVersion, "context_sha256": contextSHA}); err != nil {
		return calculations.Calculation{}, err
	}
	result, err := calc(ctx, tx, id)
	if err != nil {
		return result, err
	}
	now, err := currentTime(ctx, tx)
	if err != nil {
		return result, err
	}
	if !validGrant(g, now) {
		return calculations.Calculation{}, ErrForbidden
	}
	if err = tx.Commit(ctx); err != nil {
		return result, err
	}
	return result, nil
}
func (s *Store) UpdateSource(ctx context.Context, userID, key string, req SourceRequest) (PlanningDocument, error) {
	tx, c, g, err := s.begin(ctx, userID)
	if err != nil {
		return PlanningDocument{}, err
	}
	defer tx.Rollback(context.Background())
	if g.User.Role != "coordinator" {
		return PlanningDocument{}, ErrForbidden
	}
	if !validWindow(req.Departure, req.LoadWindow) {
		return PlanningDocument{}, ErrInvalid
	}
	h, err := hashJSON(req)
	if err != nil {
		return PlanningDocument{}, err
	}
	if _, revision, found, err := planningReplay(ctx, tx, userID, "source", key, h); err != nil {
		return PlanningDocument{}, err
	} else if found {
		d, e := planningDocument(ctx, tx, c, g)
		d.AppliedSourceRevision = revision
		return d, e
	}
	if req.ExpectedSourceRevision != c.SourceRevision || req.ExpectedAcceptedVersion != c.AcceptedVersion {
		return PlanningDocument{}, ErrConflict
	}
	source, err := readSource(ctx, tx, c.SourceRevision)
	if err != nil {
		return PlanningDocument{}, err
	}
	payload, err := changeSource(source.Payload, req.Departure, req.LoadWindow)
	if err != nil {
		return PlanningDocument{}, err
	}
	inputSHA, err := hashJSON(payload)
	if err != nil {
		return PlanningDocument{}, err
	}
	if inputSHA == source.Hash {
		return PlanningDocument{}, ErrInvalid
	}
	t, err := currentTime(ctx, tx)
	if err != nil {
		return PlanningDocument{}, err
	}
	if !validGrant(g, t) {
		return PlanningDocument{}, ErrForbidden
	}
	c.SourceRevision++
	c.ApprovalRevision++
	c.SourceChangedAt = &t
	if _, err = tx.Exec(ctx, `INSERT INTO rzd_planning_sources(workflow_id,revision,input_sha256,payload,created_at) VALUES($1,$2,$3,$4::jsonb,$5)`, WorkflowID, c.SourceRevision, inputSHA, string(payload), t); err != nil {
		return PlanningDocument{}, err
	}
	if _, err = tx.Exec(ctx, `UPDATE rzd_approval_packets SET status=CASE WHEN acceptance IS NULL THEN 'stale' ELSE 'review_required' END WHERE id=$1::uuid`, c.ActivePacketID); err != nil {
		return PlanningDocument{}, err
	}
	if err = writeContext(ctx, tx, c); err != nil {
		return PlanningDocument{}, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO rzd_planning_idempotency(actor_id,action,key,body_sha256,source_revision) VALUES($1,'source',$2,$3,$4)`, userID, key, h, c.SourceRevision); err != nil {
		return PlanningDocument{}, err
	}
	if err = audit(ctx, tx, userID, "source_payload_updated", c.ActivePacketID, map[string]any{"source_revision": c.SourceRevision, "source_sha256": inputSHA, "synthetic": true}); err != nil {
		return PlanningDocument{}, err
	}
	d, err := planningDocument(ctx, tx, c, g)
	if err != nil {
		return d, err
	}
	d.AppliedSourceRevision = &c.SourceRevision
	if err = tx.Commit(ctx); err != nil {
		return d, err
	}
	return d, nil
}
