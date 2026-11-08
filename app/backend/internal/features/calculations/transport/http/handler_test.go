package httptransport

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"rzdarctica.local/backend/internal/features/calculations"
)

type fakeService struct {
	creates int
	err     error
}

func (s *fakeService) Create(_ context.Context, _ string, scenario string) (calculations.Calculation, error) {
	s.creates++
	return calculations.Calculation{ID: "11111111-1111-4111-8111-111111111111", ScenarioID: scenario, Status: "queued"}, s.err
}
func (s *fakeService) Get(context.Context, string) (calculations.Calculation, error) {
	return calculations.Calculation{}, calculations.ErrNotFound
}
func (s *fakeService) List(context.Context) ([]calculations.Calculation, error) {
	return []calculations.Calculation{}, s.err
}
func (s *fakeService) Scenarios(context.Context) ([]calculations.Scenario, error) {
	return []calculations.Scenario{}, s.err
}

func TestCreateRequestBoundary(t *testing.T) {
	for _, tc := range []struct {
		name, body, content, key, origin string
		status                           int
	}{
		{"valid", `{"scenario_id":"S1"}`, "application/json", "same-key", "http://127.0.0.1:5173", 202},
		{"unknown-field", `{"scenario_id":"S1","accepted":true}`, "application/json", "key", "", 400},
		{"duplicate-field", `{"scenario_id":"S0","scenario_id":"S1"}`, "application/json", "key", "", 400},
		{"missing-key", `{"scenario_id":"S1"}`, "application/json", "", "", 400},
		{"unsupported-scenario", `{"scenario_id":"S9"}`, "application/json", "key", "", 400},
		{"two-json-values", `{"scenario_id":"S1"} {}`, "application/json", "key", "", 400},
		{"non-json", `scenario_id=S1`, "application/x-www-form-urlencoded", "key", "", 415},
		{"cross-origin", `{"scenario_id":"S1"}`, "application/json", "key", "https://example.com", 403},
		{"body-limit", strings.Repeat(" ", 2048) + `{"scenario_id":"S1"}`, "application/json", "key", "", 413},
		{"numeric-scenario", `{"scenario_id":1}`, "application/json", "key", "", 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service := &fakeService{}
			handler := New(service, func(context.Context) (bool, bool) { return true, true })
			r := httptest.NewRequest(http.MethodPost, "/api/v1/calculations", strings.NewReader(tc.body))
			r.Header.Set("Content-Type", tc.content)
			r.Header.Set("Idempotency-Key", tc.key)
			if tc.origin != "" {
				r.Header.Set("Origin", tc.origin)
			}
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != tc.status {
				t.Fatalf("status %d expected%d body%s", w.Code, tc.status, w.Body)
			}
			if (service.creates == 1) != (tc.status == 202) {
				t.Fatal("invalid request reached the service")
			}
		})
	}
}

func TestHistoryEmptyIsArrayAndInternalErrorIsSafe(t *testing.T) {
	service := &fakeService{}
	handler := New(service, func(context.Context) (bool, bool) { return true, true })
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/calculations", nil))
	if w.Code != 200 || strings.TrimSpace(w.Body.String()) != "[]" {
		t.Fatal("empty history not an array")
	}
	service.err = errors.New("private database connection detail")
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/calculations", nil))
	if w.Code != 500 || strings.Contains(w.Body.String(), "private") {
		t.Fatal("internal detail leaked")
	}
}

func TestReadDoesNotAcceptMalformedID(t *testing.T) {
	handler := New(&fakeService{}, func(context.Context) (bool, bool) { return true, true })
	for path, want := range map[string]int{"bad-id": 400, "11111111-1111-4111-8111-111111111111": 404} {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/calculations/"+path, nil))
		if w.Code != want {
			t.Fatalf("%s got%d", path, w.Code)
		}
	}
}
