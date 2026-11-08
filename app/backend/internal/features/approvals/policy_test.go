package approvals

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"rzdarctica.local/backend/internal/core/demoauth"
)

func policyExample() (Context, Packet, grant, map[string]grant, time.Time) {
	now := time.Date(2026, 11, 2, 15, 0, 0, 0, time.UTC)
	id := "packet"
	c := Context{SourceRevision: 1, ActivePacketID: &id, ApprovalRevision: 13}
	coord := grant{User: demoauth.User{ID: "coord", Role: "coordinator", ValidFrom: now.Add(-time.Hour), ValidUntil: now.Add(time.Hour)}, Version: 1}
	party := grant{User: demoauth.User{ID: "party", Role: "participant", OrganizationID: "SEA", Subjects: []string{"SERVICE-A"}, ValidFrom: now.Add(-time.Hour), ValidUntil: now.Add(time.Hour)}, Version: 1}
	s := Subject{ID: "SERVICE-A", OrganizationID: "SEA", LatestResponse: &Response{ID: "answer", ActorID: "party", Decision: "agree", ExpiresAt: now.Add(time.Minute), GrantVersion: 1}}
	p := Packet{ID: id, Version: 1, Status: "pending", SourceRevision: 1, PolicyID: PolicyID, Subjects: []Subject{s}}
	return c, p, coord, map[string]grant{"party": party}, now
}

func TestFinalCoverageRequiresExactCurrentAuthority(t *testing.T) {
	_, p, _, all, now := policyExample()
	s := p.Subjects[0]
	if subjectIssue(s, all, now) != "" {
		t.Fatal("valid answer rejected")
	}
	for _, mode := range []string{"missing", "rejected", "conditional", "revoked", "expiry-boundary", "grant-revoked", "grant-version", "wrong-org", "wrong-subject", "role-coordinator", "comment-on-agree"} {
		t.Run(mode, func(t *testing.T) {
			_, p, _, all, now := policyExample()
			s := p.Subjects[0]
			g := all["party"]
			switch mode {
			case "missing":
				s.LatestResponse = nil
			case "rejected", "conditional", "revoked":
				s.LatestResponse.Decision = mode
			case "expiry-boundary":
				s.LatestResponse.ExpiresAt = now
			case "grant-revoked":
				g.Revoked = true
			case "grant-version":
				g.Version++
			case "wrong-org":
				g.User.OrganizationID = "OTHER"
			case "wrong-subject":
				g.User.Subjects = []string{"SERVICE-B"}
			case "role-coordinator":
				g.User.Role = "coordinator"
			case "comment-on-agree":
				s.LatestResponse.Comment = "только если другое окно"
			}
			all["party"] = g
			if subjectIssue(s, all, now) == "" {
				t.Fatal("unsafe answer counted")
			}
		})
	}
}
func TestReadProjectionAndAcceptanceHints(t *testing.T) {
	c, p, coord, all, now := policyExample()
	view, err := decorate(c, p, coord, all, now)
	if err != nil || !view.CanAccept {
		t.Fatal("ready coordinator", err)
	}
	if view.Subjects[0].CanRespond {
		t.Fatal("coordinator may not answer for party")
	}
	other := Subject{ID: "RECIPIENT-P1", OrganizationID: "RECIPIENT-P1"}
	p.Subjects = append(p.Subjects, other)
	p.FixedContext = []byte(`{"private":true}`)
	p.Binding = map[string]any{"private": true}
	view, err = decorate(c, p, all["party"], all, now)
	if err != nil || len(view.Subjects) != 1 || view.Subjects[0].ID != "SERVICE-A" || view.FixedContext != nil || view.Binding != nil || view.CanAccept {
		t.Fatal("foreign details visible", err)
	}
	for _, issue := range view.Issues {
		if strings.Contains(issue, "RECIPIENT") {
			t.Fatal("foreign issue visible")
		}
	}
	if !view.Subjects[0].CanRespond {
		t.Fatal("own participant cannot respond")
	}
	_, p, _, all, now = policyExample()
	p.Acceptance = &Acceptance{ID: "act"}
	p.Status = "accepted"
	p.Subjects[0].LatestResponse.Decision = "revoked"
	view, err = decorate(c, p, coord, all, now)
	if err != nil || view.Status != "review_required" || view.Acceptance.ID != "act" {
		t.Fatal("historical act lost", err)
	}
}
func TestStaleHeadsCannotBeAccepted(t *testing.T) {
	for _, mode := range []string{"source", "head", "superseded", "policy"} {
		t.Run(mode, func(t *testing.T) {
			c, p, g, all, now := policyExample()
			switch mode {
			case "source":
				c.SourceRevision++
			case "head":
				c.AcceptedVersion++
			case "superseded":
				other := "other"
				c.ActivePacketID = &other
			case "policy":
				p.PolicyID = "other"
			}
			v, err := decorate(c, p, g, all, now)
			if err != nil || v.CanAccept {
				t.Fatal("stale packet ready", err)
			}
		})
	}
}
func TestStrictRequestDecoder(t *testing.T) {
	for _, body := range []string{`{"expected_source_revision":1,"actor_id":"coord"}`, `{"expected_source_revision":1,"expected_source_revision":1}`, `{"expected_source_revision":1} {}`, `null`, `{"expected_source_revision":1.5}`, string([]byte{'{', '"', 'x', '"', ':', '"', 255, '"', '}'})} {
		r := httptest.NewRequest("POST", "http://127.0.0.1:8090/api/v1/approval-context/invalidate", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Idempotency-Key", "unit-case")
		w := httptest.NewRecorder()
		var req InvalidateRequest
		if parse(w, r, &req) || w.Code != 400 {
			t.Fatal("accepted malformed input", body, w.Code)
		}
	}
	r := httptest.NewRequest("POST", "http://127.0.0.1:8090/api/v1/approval-context/invalidate", strings.NewReader(`{"expected_source_revision":1}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Idempotency-Key", "unit-case")
	w := httptest.NewRecorder()
	var req InvalidateRequest
	if !parse(w, r, &req) || req.ExpectedSourceRevision != 1 {
		t.Fatal("valid JSON rejected")
	}
	r = httptest.NewRequest("POST", "http://127.0.0.1:8090/api/v1/approval-context/invalidate", strings.NewReader(`{"expected_source_revision":1}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Idempotency-Key", "unit-case")
	r.Header.Set("Origin", "https://example.com")
	w = httptest.NewRecorder()
	if parse(w, r, &req) || w.Code != 403 {
		t.Fatal("foreign origin accepted")
	}
}
