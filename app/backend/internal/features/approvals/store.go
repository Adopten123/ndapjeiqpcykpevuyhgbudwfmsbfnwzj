package approvals

import (
	"context"
	"crypto/rand"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"rzdarctica.local/backend/internal/core/demoauth"
	"rzdarctica.local/backend/internal/features/calculations"
)

//go:embed schema.sql
var schema string

type Store struct {
	pool  *pgxpool.Pool
	queue *river.Client[pgx.Tx]
}

func (s *Store) SetQueue(queue *river.Client[pgx.Tx]) { s.queue = queue }

type grant struct {
	User    demoauth.User
	Revoked bool
	Version int64
}

func New(pool *pgxpool.Pool) *Store                { return &Store{pool: pool} }
func (s *Store) Migrate(ctx context.Context) error { _, err := s.pool.Exec(ctx, schema); return err }
func (s *Store) Seed(ctx context.Context, users []demoauth.User) error {
	for _, u := range users {
		_, err := s.pool.Exec(ctx, `INSERT INTO rzd_approval_grants(user_id,name,role,organization_id,subjects,valid_from,valid_until) VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT(user_id) DO NOTHING`, u.ID, u.Name, u.Role, u.OrganizationID, u.Subjects, u.ValidFrom, u.ValidUntil)
		if err != nil {
			return err
		}
	}
	return nil
}
func newID() (string, error) {
	var v [16]byte
	if _, err := rand.Read(v[:]); err != nil {
		return "", err
	}
	v[6] = v[6]&15 | 64
	v[8] = v[8]&63 | 128
	return fmt.Sprintf("%x-%x-%x-%x-%x", v[:4], v[4:6], v[6:8], v[8:10], v[10:]), nil
}
func readContext(ctx context.Context, tx pgx.Tx) (Context, error) {
	var c Context
	err := tx.QueryRow(ctx, `SELECT source_revision,approval_revision,packet_version,active_packet_id::text,accepted_packet_id::text,accepted_version,source_changed_at FROM rzd_approval_context WHERE workflow_id=$1 FOR UPDATE`, WorkflowID).Scan(&c.SourceRevision, &c.ApprovalRevision, &c.PacketVersion, &c.ActivePacketID, &c.AcceptedPacketID, &c.AcceptedVersion, &c.SourceChangedAt)
	return c, err
}
func readGrant(ctx context.Context, tx pgx.Tx, id string) (grant, error) {
	var g grant
	err := tx.QueryRow(ctx, `SELECT user_id,name,role,organization_id,subjects,valid_from,valid_until,revoked,version FROM rzd_approval_grants WHERE user_id=$1 FOR SHARE`, id).Scan(&g.User.ID, &g.User.Name, &g.User.Role, &g.User.OrganizationID, &g.User.Subjects, &g.User.ValidFrom, &g.User.ValidUntil, &g.Revoked, &g.Version)
	if errors.Is(err, pgx.ErrNoRows) {
		return g, ErrForbidden
	}
	return g, err
}
func currentTime(ctx context.Context, tx pgx.Tx) (time.Time, error) {
	var t time.Time
	err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&t)
	return t, err
}
func validGrant(g grant, t time.Time) bool {
	return !g.Revoked && !t.Before(g.User.ValidFrom) && t.Before(g.User.ValidUntil)
}
func owns(g grant, s Subject) bool {
	if g.User.Role != "participant" || g.User.OrganizationID != s.OrganizationID {
		return false
	}
	for _, id := range g.User.Subjects {
		if id == s.ID {
			return true
		}
	}
	return false
}
func (s *Store) begin(ctx context.Context, userID string) (pgx.Tx, Context, grant, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, Context{}, grant{}, err
	}
	c, err := readContext(ctx, tx)
	var g grant
	if err == nil {
		g, err = readGrant(ctx, tx, userID)
	}
	if err == nil {
		var t time.Time
		t, err = currentTime(ctx, tx)
		if err == nil && !validGrant(g, t) {
			err = ErrForbidden
		}
	}
	if err != nil {
		tx.Rollback(context.Background())
		return nil, c, g, err
	}
	return tx, c, g, nil
}
func writeContext(ctx context.Context, tx pgx.Tx, c Context) error {
	_, err := tx.Exec(ctx, `UPDATE rzd_approval_context SET source_revision=$2,approval_revision=$3,packet_version=$4,active_packet_id=$5::uuid,accepted_packet_id=$6::uuid,accepted_version=$7,source_changed_at=$8 WHERE workflow_id=$1`, WorkflowID, c.SourceRevision, c.ApprovalRevision, c.PacketVersion, c.ActivePacketID, c.AcceptedPacketID, c.AcceptedVersion, c.SourceChangedAt)
	return err
}
func packetByID(ctx context.Context, tx pgx.Tx, id string) (Packet, error) {
	var p Packet
	var raw, act []byte
	var status string
	err := tx.QueryRow(ctx, `SELECT content,status,acceptance FROM rzd_approval_packets WHERE id=$1::uuid`, id).Scan(&raw, &status, &act)
	if errors.Is(err, pgx.ErrNoRows) {
		return p, ErrNotFound
	}
	if err != nil {
		return p, err
	}
	if err = json.Unmarshal(raw, &p); err != nil {
		return p, err
	}
	p.Status = status
	if len(act) > 0 {
		var a Acceptance
		if err = json.Unmarshal(act, &a); err != nil {
			return p, err
		}
		p.Acceptance = &a
	}
	return p, nil
}
func loadResponses(ctx context.Context, tx pgx.Tx, p *Packet) error {
	rows, err := tx.Query(ctx, `SELECT DISTINCT ON(subject_id) subject_id,body,revision,actor_id,grant_version,decision,expires_at,created_at FROM rzd_approval_responses WHERE packet_id=$1::uuid ORDER BY subject_id,revision DESC`, p.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	responses := map[string]Response{}
	for rows.Next() {
		var r Response
		var raw []byte
		var subject string
		var rev, gv int64
		var actor, decision string
		var expiry, created time.Time
		if err = rows.Scan(&subject, &raw, &rev, &actor, &gv, &decision, &expiry, &created); err != nil {
			return err
		}
		if err = json.Unmarshal(raw, &r); err != nil {
			return err
		}
		r.SubjectID = subject
		r.Revision = rev
		r.ActorID = actor
		r.GrantVersion = gv
		r.Decision = decision
		r.ExpiresAt = expiry
		r.CreatedAt = created
		responses[subject] = r
	}
	if err = rows.Err(); err != nil {
		return err
	}
	for i := range p.Subjects {
		if r, ok := responses[p.Subjects[i].ID]; ok {
			p.Subjects[i].LatestResponse = &r
			p.Subjects[i].Revision = r.Revision
		}
	}
	return nil
}
func packetGrants(ctx context.Context, tx pgx.Tx, p Packet) (map[string]grant, error) {
	ids := map[string]bool{}
	for _, s := range p.Subjects {
		if s.LatestResponse != nil {
			ids[s.LatestResponse.ActorID] = true
		}
	}
	// A stable order also permits an administrator to lock grant rows consistently.
	rows, err := tx.Query(ctx, `SELECT user_id,name,role,organization_id,subjects,valid_from,valid_until,revoked,version FROM rzd_approval_grants WHERE user_id=ANY($1::text[]) ORDER BY user_id FOR SHARE`, keys(ids))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	all := map[string]grant{}
	for rows.Next() {
		var g grant
		if err = rows.Scan(&g.User.ID, &g.User.Name, &g.User.Role, &g.User.OrganizationID, &g.User.Subjects, &g.User.ValidFrom, &g.User.ValidUntil, &g.Revoked, &g.Version); err != nil {
			return nil, err
		}
		all[g.User.ID] = g
	}
	return all, rows.Err()
}
func keys(m map[string]bool) []string {
	v := []string{}
	for k := range m {
		v = append(v, k)
	}
	return v
}
func subjectIssue(s Subject, grants map[string]grant, t time.Time) string {
	r := s.LatestResponse
	if r == nil {
		return "Нет ответа"
	}
	if r.Decision != "agree" {
		switch r.Decision {
		case "rejected":
			return "Отказ"
		case "conditional":
			return "Встречные условия"
		case "revoked":
			return "Ответ отозван"
		}
		return "Нет безусловного согласия"
	}
	if strings.TrimSpace(r.Comment) != "" {
		return "У ответа есть дополнительные условия или пояснение"
	}
	if !t.Before(r.ExpiresAt) {
		return "Срок ответа истёк"
	}
	g, ok := grants[r.ActorID]
	if !ok || !validGrant(g, t) || g.Version != r.GrantVersion || !owns(g, s) {
		return "Назначение отвечающего недействительно"
	}
	return ""
}
func decorate(c Context, p Packet, g grant, all map[string]grant, t time.Time) (Packet, error) {
	p.ApprovalRevision = c.ApprovalRevision
	p.Issues = []string{}
	p.CanAccept = false
	active := c.ActivePacketID != nil && *c.ActivePacketID == p.ID
	acceptedHead := p.Acceptance != nil && c.AcceptedPacketID != nil && *c.AcceptedPacketID == p.ID
	fresh := p.SourceRevision == c.SourceRevision && p.PolicyID == PolicyID
	if !active && p.Status == "pending" {
		p.Status = "superseded"
	}
	if !fresh {
		p.Issues = append(p.Issues, "Учебные исходные условия изменились")
		if p.Acceptance != nil {
			p.Status = "review_required"
		} else if p.Status == "pending" {
			p.Status = "stale"
		}
	}
	fullCoverage := true
	filtered := []Subject{}
	for _, s := range p.Subjects {
		issue := subjectIssue(s, all, t)
		if issue != "" {
			fullCoverage = false
		}
		visible := g.User.Role != "participant" || owns(g, s)
		if !visible {
			continue
		}
		s.CanRespond = owns(g, s) && (active && fresh && p.Status == "pending" || acceptedHead)
		if issue != "" {
			p.Issues = append(p.Issues, s.ID+": "+issue)
		}
		filtered = append(filtered, s)
	}
	if g.User.Role == "participant" {
		if len(filtered) == 0 {
			return p, ErrNotFound
		}
		p.FixedContext = nil
		p.Binding = nil
	}
	if p.Acceptance != nil && !fullCoverage {
		p.Status = "review_required"
	}
	if g.User.Role == "coordinator" && p.Status == "pending" && active && fresh && p.BaseAcceptedVersion == c.AcceptedVersion && fullCoverage {
		p.CanAccept = true
	}
	if g.User.Role != "participant" && p.Status == "pending" && p.BaseAcceptedVersion != c.AcceptedVersion {
		p.Issues = append(p.Issues, "Принятая голова плана изменилась")
	}
	p.Subjects = filtered
	return p, nil
}
func document(ctx context.Context, tx pgx.Tx, c Context, g grant, id string) (Document, error) {
	p, err := packetByID(ctx, tx, id)
	if err != nil {
		return Document{}, err
	}
	if err = loadResponses(ctx, tx, &p); err != nil {
		return Document{}, err
	}
	all, err := packetGrants(ctx, tx, p)
	if err != nil {
		return Document{}, err
	}
	t, err := currentTime(ctx, tx)
	if err != nil {
		return Document{}, err
	}
	if !validGrant(g, t) {
		return Document{}, ErrForbidden
	}
	p, err = decorate(c, p, g, all, t)
	if err != nil {
		return Document{}, err
	}
	return Document{Context: c, Packet: &p}, nil
}
func (s *Store) Get(ctx context.Context, userID, id string) (Document, error) {
	tx, c, g, err := s.begin(ctx, userID)
	if err != nil {
		return Document{}, err
	}
	defer tx.Rollback(context.Background())
	return document(ctx, tx, c, g, id)
}
func (s *Store) List(ctx context.Context, userID string) (Listing, error) {
	tx, c, g, err := s.begin(ctx, userID)
	if err != nil {
		return Listing{}, err
	}
	defer tx.Rollback(context.Background())
	rows, err := tx.Query(ctx, `SELECT id::text FROM rzd_approval_packets ORDER BY version DESC LIMIT 20`)
	if err != nil {
		return Listing{}, err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return Listing{}, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if rows.Err() != nil {
		return Listing{}, rows.Err()
	}
	out := Listing{Context: c, Packets: []Packet{}}
	for _, id := range ids {
		d, err := document(ctx, tx, c, g, id)
		if errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			return out, err
		}
		p := *d.Packet
		p.Subjects = []Subject{}
		p.FixedContext = nil
		p.Binding = nil
		out.Packets = append(out.Packets, p)
	}
	return out, nil
}
func replay(ctx context.Context, tx pgx.Tx, actor, action, key, hash string) (*string, bool, error) {
	var old string
	var id *string
	err := tx.QueryRow(ctx, `SELECT body_sha256,packet_id::text FROM rzd_approval_idempotency WHERE actor_id=$1 AND action=$2 AND key=$3`, actor, action, key).Scan(&old, &id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if old != hash {
		return nil, false, ErrConflict
	}
	return id, true, nil
}
func remember(ctx context.Context, tx pgx.Tx, actor, action, key, hash string, id *string) error {
	_, err := tx.Exec(ctx, `INSERT INTO rzd_approval_idempotency(actor_id,action,key,body_sha256,packet_id) VALUES($1,$2,$3,$4,$5::uuid)`, actor, action, key, hash, id)
	return err
}
func audit(ctx context.Context, tx pgx.Tx, actor, action string, id *string, detail any) error {
	raw, err := json.Marshal(detail)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO rzd_approval_audit(workflow_id,actor_id,action,packet_id,details) VALUES($1,$2,$3,$4::uuid,$5::jsonb)`, WorkflowID, actor, action, id, string(raw))
	return err
}
func calc(ctx context.Context, tx pgx.Tx, id string) (calculations.Calculation, error) {
	var c calculations.Calculation
	err := tx.QueryRow(ctx, `SELECT id::text,scenario_id,status,engine_profile,snapshot_id,input_sha256,input_payload,result,created_at,updated_at,error,source_revision,base_accepted_version,base_accepted_packet_id::text,source_sha256,baseline_sha256,context_sha256 FROM rzd_calculations WHERE id=$1::uuid`, id).Scan(&c.ID, &c.ScenarioID, &c.Status, &c.EngineProfile, &c.SnapshotID, &c.InputSHA256, &c.Payload, &c.Result, &c.CreatedAt, &c.UpdatedAt, &c.Error, &c.SourceRevision, &c.BaseAcceptedVersion, &c.BaseAcceptedPacketID, &c.SourceSHA256, &c.BaselineSHA256, &c.ContextSHA256)
	if errors.Is(err, pgx.ErrNoRows) {
		return c, ErrNotFound
	}
	return c, err
}

func (s *Store) Create(ctx context.Context, userID, key string, req CreateRequest) (Document, error) {
	tx, c, g, err := s.begin(ctx, userID)
	if err != nil {
		return Document{}, err
	}
	defer tx.Rollback(context.Background())
	if g.User.Role != "coordinator" {
		return Document{}, ErrForbidden
	}
	h, err := hashJSON(req)
	if err != nil {
		return Document{}, err
	}
	if id, found, err := replay(ctx, tx, userID, "create", key, h); err != nil {
		return Document{}, err
	} else if found {
		return document(ctx, tx, c, g, *id)
	}
	if req.ExpectedSourceRevision != c.SourceRevision || req.ExpectedAcceptedVersion != c.AcceptedVersion {
		return Document{}, ErrConflict
	}
	calculation, err := calc(ctx, tx, req.CalculationID)
	if err != nil {
		return Document{}, err
	}
	if err = validateBinding(ctx, tx, c, calculation); err != nil {
		return Document{}, err
	}
	p, err := BuildPacket(calculation)
	if err != nil {
		return Document{}, err
	}
	p.ID, err = newID()
	if err != nil {
		return Document{}, err
	}
	p.CreatedAt, err = currentTime(ctx, tx)
	if err != nil {
		return Document{}, err
	}
	if !validGrant(g, p.CreatedAt) {
		return Document{}, ErrForbidden
	}
	p.Version = c.PacketVersion + 1
	p.SourceRevision = c.SourceRevision
	p.BaseAcceptedVersion = c.AcceptedVersion
	p.ApprovalRevision = c.ApprovalRevision
	raw, err := json.Marshal(p)
	if err != nil {
		return Document{}, err
	}
	if c.ActivePacketID != nil {
		_, err = tx.Exec(ctx, `UPDATE rzd_approval_packets SET status='superseded' WHERE id=$1::uuid AND status IN ('pending','stale')`, *c.ActivePacketID)
		if err != nil {
			return Document{}, err
		}
	}
	_, err = tx.Exec(ctx, `INSERT INTO rzd_approval_packets(id,version,calculation_id,source_revision,base_accepted_version,status,created_at,content) VALUES($1::uuid,$2,$3::uuid,$4,$5,'pending',$6,$7::jsonb)`, p.ID, p.Version, p.CalculationID, p.SourceRevision, p.BaseAcceptedVersion, p.CreatedAt, string(raw))
	if err != nil {
		return Document{}, err
	}
	c.ActivePacketID = &p.ID
	c.PacketVersion = p.Version
	c.ApprovalRevision++
	if err = writeContext(ctx, tx, c); err != nil {
		return Document{}, err
	}
	if err = remember(ctx, tx, userID, "create", key, h, &p.ID); err != nil {
		return Document{}, err
	}
	if err = audit(ctx, tx, userID, "packet_created", &p.ID, map[string]any{"calculation_id": p.CalculationID, "candidate_sha256": p.CandidateSHA256, "source_revision": c.SourceRevision}); err != nil {
		return Document{}, err
	}
	d, err := document(ctx, tx, c, g, p.ID)
	if err != nil {
		return d, err
	}
	if err = tx.Commit(ctx); err != nil {
		return Document{}, err
	}
	return d, nil
}

func (s *Store) Respond(ctx context.Context, userID, key, id string, req RespondRequest) (Document, error) {
	tx, c, g, err := s.begin(ctx, userID)
	if err != nil {
		return Document{}, err
	}
	defer tx.Rollback(context.Background())
	p, err := packetByID(ctx, tx, id)
	if err != nil {
		return Document{}, err
	}
	var subject *Subject
	for i := range p.Subjects {
		if p.Subjects[i].ID == req.SubjectID {
			subject = &p.Subjects[i]
			break
		}
	}
	if subject == nil || !owns(g, *subject) {
		return Document{}, ErrForbidden
	}
	h, err := hashJSON(req)
	if err != nil {
		return Document{}, err
	}
	action := "response:" + id
	if _, found, err := replay(ctx, tx, userID, action, key, h); err != nil {
		return Document{}, err
	} else if found {
		return document(ctx, tx, c, g, id)
	}
	active := c.ActivePacketID != nil && *c.ActivePacketID == id && p.SourceRevision == c.SourceRevision
	acceptedHead := p.Acceptance != nil && c.AcceptedPacketID != nil && *c.AcceptedPacketID == id && req.Decision != "agree"
	if (!active && !acceptedHead) || p.Version != req.PacketVersion || p.PolicyID != PolicyID || subject.SHA256 != req.SubjectSHA256 {
		return Document{}, ErrConflict
	}
	if p.Status != "pending" && p.Status != "accepted" && p.Status != "review_required" {
		return Document{}, ErrConflict
	}
	if req.Decision != "agree" && req.Decision != "rejected" && req.Decision != "conditional" && req.Decision != "revoked" {
		return Document{}, ErrInvalid
	}
	if p.Acceptance != nil && req.Decision == "agree" {
		return Document{}, ErrConflict
	}
	if len(req.Comment) > 1000 {
		return Document{}, ErrInvalid
	}
	if req.Decision == "agree" && strings.TrimSpace(req.Comment) != "" {
		return Document{}, ErrInvalid
	}
	if err = loadResponses(ctx, tx, &p); err != nil {
		return Document{}, err
	}
	for _, s := range p.Subjects {
		if s.ID == req.SubjectID && s.Revision != req.ExpectedRevision {
			return Document{}, ErrConflict
		}
	}
	t, err := currentTime(ctx, tx)
	if err != nil {
		return Document{}, err
	}
	if !validGrant(g, t) {
		return Document{}, ErrForbidden
	}
	if !t.Before(req.ExpiresAt) || req.ExpiresAt.After(g.User.ValidUntil) {
		return Document{}, ErrInvalid
	}
	rid, err := newID()
	if err != nil {
		return Document{}, err
	}
	response := Response{ID: rid, SubjectID: req.SubjectID, Decision: req.Decision, ExpiresAt: req.ExpiresAt, ActorID: userID, Revision: req.ExpectedRevision + 1, CreatedAt: t, Comment: req.Comment, GrantVersion: g.Version}
	raw, err := json.Marshal(response)
	if err != nil {
		return Document{}, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO rzd_approval_responses(id,packet_id,subject_id,revision,actor_id,grant_version,decision,expires_at,created_at,body) VALUES($1::uuid,$2::uuid,$3,$4,$5,$6,$7,$8,$9,$10::jsonb)`, rid, id, req.SubjectID, response.Revision, userID, g.Version, req.Decision, req.ExpiresAt, t, string(raw))
	if err != nil {
		return Document{}, err
	}
	c.ApprovalRevision++
	if p.Acceptance != nil {
		_, err = tx.Exec(ctx, `UPDATE rzd_approval_packets SET status='review_required' WHERE id=$1::uuid`, id)
		if err != nil {
			return Document{}, err
		}
	}
	if err = writeContext(ctx, tx, c); err != nil {
		return Document{}, err
	}
	if err = remember(ctx, tx, userID, action, key, h, &id); err != nil {
		return Document{}, err
	}
	if err = audit(ctx, tx, userID, "subject_response", &id, map[string]any{"response_id": rid, "subject_id": req.SubjectID, "revision": response.Revision, "decision": req.Decision}); err != nil {
		return Document{}, err
	}
	d, err := document(ctx, tx, c, g, id)
	if err != nil {
		return d, err
	}
	if err = tx.Commit(ctx); err != nil {
		return Document{}, err
	}
	return d, nil
}

func (s *Store) Accept(ctx context.Context, userID, key, id string, req AcceptRequest) (Document, error) {
	tx, c, g, err := s.begin(ctx, userID)
	if err != nil {
		return Document{}, err
	}
	defer tx.Rollback(context.Background())
	if g.User.Role != "coordinator" {
		return Document{}, ErrForbidden
	}
	h, err := hashJSON(req)
	if err != nil {
		return Document{}, err
	}
	action := "accept:" + id
	if _, found, err := replay(ctx, tx, userID, action, key, h); err != nil {
		return Document{}, err
	} else if found {
		return document(ctx, tx, c, g, id)
	}
	p, err := packetByID(ctx, tx, id)
	if err != nil {
		return Document{}, err
	}
	if p.Status != "pending" || p.Acceptance != nil || c.ActivePacketID == nil || *c.ActivePacketID != id || p.Version != req.PacketVersion || p.SourceRevision != c.SourceRevision || req.ExpectedSourceRevision != c.SourceRevision || req.ExpectedAcceptedVersion != c.AcceptedVersion || p.BaseAcceptedVersion != c.AcceptedVersion || req.ExpectedApprovalRevision != c.ApprovalRevision || p.PolicyID != PolicyID {
		return Document{}, ErrConflict
	}
	calculation, err := calc(ctx, tx, p.CalculationID)
	if err != nil {
		return Document{}, err
	}
	if err = validateBinding(ctx, tx, c, calculation); err != nil {
		return Document{}, ErrConflict
	}
	fresh, err := BuildPacket(calculation)
	if err != nil {
		return Document{}, ErrConflict
	}
	if fresh.InputSHA256 != p.InputSHA256 || fresh.CandidateSHA256 != p.CandidateSHA256 || fresh.SnapshotID != p.SnapshotID || len(fresh.Subjects) != len(p.Subjects) {
		return Document{}, ErrConflict
	}
	for i := range p.Subjects {
		if p.Subjects[i].ID != fresh.Subjects[i].ID || p.Subjects[i].SHA256 != fresh.Subjects[i].SHA256 {
			return Document{}, ErrConflict
		}
	}
	if err = loadResponses(ctx, tx, &p); err != nil {
		return Document{}, err
	}
	all, err := packetGrants(ctx, tx, p)
	if err != nil {
		return Document{}, err
	}
	// PostgreSQL now() is the transaction start: recheck wall time after all locks.
	t, err := currentTime(ctx, tx)
	if err != nil {
		return Document{}, err
	}
	if !validGrant(g, t) {
		return Document{}, ErrForbidden
	}
	for _, s := range p.Subjects {
		if subjectIssue(s, all, t) != "" {
			return Document{}, ErrConflict
		}
	}
	aid, err := newID()
	if err != nil {
		return Document{}, err
	}
	a := Acceptance{ID: aid, ActorID: userID, AcceptedAt: t, AcceptedVersion: c.AcceptedVersion + 1}
	raw, err := json.Marshal(a)
	if err != nil {
		return Document{}, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO rzd_approval_acts(id,packet_id,accepted_version,actor_id,accepted_at,body) VALUES($1::uuid,$2::uuid,$3,$4,$5,$6::jsonb)`, aid, id, a.AcceptedVersion, userID, t, string(raw))
	if err != nil {
		return Document{}, err
	}
	_, err = tx.Exec(ctx, `UPDATE rzd_approval_packets SET status='accepted',acceptance=$2::jsonb WHERE id=$1::uuid`, id, string(raw))
	if err != nil {
		return Document{}, err
	}
	c.AcceptedVersion = a.AcceptedVersion
	c.AcceptedPacketID = &id
	c.ApprovalRevision++
	if err = writeContext(ctx, tx, c); err != nil {
		return Document{}, err
	}
	if err = remember(ctx, tx, userID, action, key, h, &id); err != nil {
		return Document{}, err
	}
	if err = audit(ctx, tx, userID, "plan_accepted", &id, a); err != nil {
		return Document{}, err
	}
	d, err := document(ctx, tx, c, g, id)
	if err != nil {
		return d, err
	}
	if err = tx.Commit(ctx); err != nil {
		return Document{}, err
	}
	return d, nil
}

func (s *Store) Invalidate(ctx context.Context, userID, key string, req InvalidateRequest) (Document, error) {
	tx, _, g, err := s.begin(ctx, userID)
	if err != nil {
		return Document{}, err
	}
	defer tx.Rollback(context.Background())
	if g.User.Role != "coordinator" {
		return Document{}, ErrForbidden
	}
	return Document{}, ErrSourceMutationRequired
}
