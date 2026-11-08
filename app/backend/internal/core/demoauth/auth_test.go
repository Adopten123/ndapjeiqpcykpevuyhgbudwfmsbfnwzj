package demoauth

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const testAccessKey = "unit-test-access-key-not-an-operational-secret"

func testConfig(t *testing.T) ([]byte, time.Time) {
	t.Helper()
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	hash := sha256.Sum256([]byte(testAccessKey))
	data, err := json.Marshal(privateConfig{Scope: Scope, Users: []credential{{User: User{ID: "coordinator.demo", Name: "Учебный координатор", Role: "coordinator", OrganizationID: "COORD", Subjects: []string{"*"}, ValidFrom: now.Add(-time.Minute), ValidUntil: now.Add(24 * time.Hour)}, TokenSHA256: hex.EncodeToString(hash[:])}}})
	if err != nil {
		t.Fatal(err)
	}
	return data, now
}

func loadTest(t *testing.T, data []byte) *Registry {
	t.Helper()
	path := filepath.Join(t.TempDir(), "auth.private.json")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	r, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func testRegistry(t *testing.T) (*Registry, time.Time) {
	t.Helper()
	data, now := testConfig(t)
	r := loadTest(t, data)
	r.now = func() time.Time { return now }
	return r, now
}

func request(handler http.Handler, method, path, body, origin string, cookie *http.Cookie) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if cookie != nil {
		req.AddCookie(cookie)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func login(t *testing.T, r *Registry) *http.Cookie {
	t.Helper()
	response := request(r.SessionHandler(), "POST", "/api/v1/session", `{"access_key":"`+testAccessKey+`"}`, "http://127.0.0.1:5173", nil)
	if response.Code != 200 {
		t.Fatalf("login status %d", response.Code)
	}
	cookies := response.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatal("missing session cookie")
	}
	return cookies[0]
}

func TestIsolatedOriginLoginAndCookieMutation(t *testing.T) {
	t.Setenv("RZD_UI_ORIGIN", "http://127.0.0.1:5183")
	r, _ := testRegistry(t)
	out := request(r.SessionHandler(), "POST", "/api/v1/session", `{"access_key":"`+testAccessKey+`"}`, "http://127.0.0.1:5183", nil)
	if out.Code != 200 {
		t.Fatalf("isolated login status %d", out.Code)
	}
	cookie := out.Result().Cookies()[0]
	if !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode {
		t.Fatal("cookie protections weakened")
	}
	handler := r.Middleware(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) { w.WriteHeader(204) }))
	if out = request(handler, "POST", "/api/test", "", "http://127.0.0.1:5183", cookie); out.Code != 204 {
		t.Fatal("configured mutation denied")
	}
	for _, origin := range []string{"", "http://127.0.0.1:5184", "http://localhost:5183", "https://example.com"} {
		if out = request(handler, "POST", "/api/test", "", origin, cookie); out.Code != 403 {
			t.Fatal("unconfigured cookie origin accepted")
		}
	}
}

func TestPrivateConfigIsClosedAndUnique(t *testing.T) {
	base, _ := testConfig(t)
	mutations := map[string]func(map[string]any){
		"unknown-top-field":  func(c map[string]any) { c["access_key"] = "must-never-be-returned" },
		"wrong-scope":        func(c map[string]any) { c["scope"] = "production" },
		"unknown-user-field": func(c map[string]any) { c["users"].([]any)[0].(map[string]any)["admin"] = true },
		"wrong-field-case": func(c map[string]any) {
			u := c["users"].([]any)[0].(map[string]any)
			u["ID"] = u["id"]
			delete(u, "id")
		},
		"plaintext-key-forbidden": func(c map[string]any) { c["users"].([]any)[0].(map[string]any)["access_key"] = testAccessKey },
		"invalid-role":            func(c map[string]any) { c["users"].([]any)[0].(map[string]any)["role"] = "administrator" },
		"invalid-hash":            func(c map[string]any) { c["users"].([]any)[0].(map[string]any)["token_sha256"] = "no" },
		"invalid-validity":        func(c map[string]any) { c["users"].([]any)[0].(map[string]any)["valid_until"] = "2026-11-01T00:00:00Z" },
		"duplicate-id":            func(c map[string]any) { c["users"] = append(c["users"].([]any), c["users"].([]any)[0]) },
		"participant-wildcard":    func(c map[string]any) { c["users"].([]any)[0].(map[string]any)["role"] = "participant" },
		"duplicate-subject":       func(c map[string]any) { c["users"].([]any)[0].(map[string]any)["subjects"] = []any{"*", "*"} },
		"missing-subjects":        func(c map[string]any) { delete(c["users"].([]any)[0].(map[string]any), "subjects") },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			var cfg map[string]any
			if err := json.Unmarshal(base, &cfg); err != nil {
				t.Fatal(err)
			}
			mutate(cfg)
			data, _ := json.Marshal(cfg)
			path := filepath.Join(t.TempDir(), "bad.json")
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(path); err == nil || strings.Contains(err.Error(), testAccessKey) {
				t.Fatal("bad configuration accepted or leaked")
			}
		})
	}
	for _, data := range [][]byte{[]byte(`{"scope":"demo-chain","scope":"demo-chain","users":[]}`), append(append([]byte{}, base...), []byte(`{}`)...)} {
		path := filepath.Join(t.TempDir(), "ambiguous.json")
		os.WriteFile(path, data, 0600)
		if _, err := Load(path); err == nil {
			t.Fatal("ambiguous private JSON accepted")
		}
	}
	if _, err := Load(""); err == nil {
		t.Fatal("empty path must stop startup")
	}
	if _, err := Load(filepath.Join(t.TempDir(), "absent")); err == nil {
		t.Fatal("missing configuration must stop startup")
	}
}

func TestBearerIdentityAndDefensiveCopies(t *testing.T) {
	r, now := testRegistry(t)
	users := r.Users()
	users[0].Subjects[0] = "FORGED"
	if r.Users()[0].Subjects[0] != "*" {
		t.Fatal("caller mutated registry subjects")
	}
	handler := r.Middleware(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		u, ok := UserFromContext(req.Context())
		if !ok || u.ID != "coordinator.demo" || u.OrganizationID != "COORD" {
			t.Error("missing trusted identity")
		}
		write(w, 200, map[string]any{"user": u})
	}))
	for _, auth := range []string{"", "Bearer wrong", "Basic " + testAccessKey, "Bearer " + testAccessKey + " extra"} {
		req := httptest.NewRequest("POST", "/api/test", nil)
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		if response.Code != 401 {
			t.Fatalf("invalid auth accepted: status%d", response.Code)
		}
	}
	req := httptest.NewRequest("POST", "/api/test", nil)
	req.Header.Set("Authorization", "Bearer "+testAccessKey)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, req)
	if response.Code != 200 || strings.Contains(response.Body.String(), "token_sha256") || strings.Contains(response.Body.String(), testAccessKey) {
		t.Fatal("bearer failed or credential leaked")
	}
	r.now = func() time.Time { return now.Add(24 * time.Hour) }
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, req)
	if response.Code != 401 {
		t.Fatal("access key valid at exact expiry")
	}
}

func TestSessionLoginCookieLogoutAndExpiry(t *testing.T) {
	r, now := testRegistry(t)
	cookie := login(t, r)
	if !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode || cookie.Path != "/api" || cookie.MaxAge != 3600 || cookie.Secure {
		t.Fatal("unexpected local cookie attributes")
	}
	if cookie.Value == testAccessKey {
		t.Fatal("session reuses access key")
	}
	for _, mode := range []string{"valid", "revoked", "expired", "key-expired"} {
		t.Run(mode, func(t *testing.T) {
			r.now = func() time.Time { return now }
			cookie := login(t, r)
			if mode == "revoked" {
				out := request(r.SessionHandler(), "DELETE", "/api/v1/session", "", "http://127.0.0.1:5173", cookie)
				if out.Code != 204 || out.Result().Cookies()[0].MaxAge != -1 {
					t.Fatal("logout failed")
				}
			}
			if mode == "expired" {
				r.now = func() time.Time { return now.Add(time.Hour) }
			}
			if mode == "key-expired" {
				r.now = func() time.Time { return now.Add(24 * time.Hour) }
			}
			out := request(r.SessionHandler(), "GET", "/api/v1/session", "", "", cookie)
			want := 401
			if mode == "valid" {
				want = 200
			}
			if out.Code != want {
				t.Fatalf("session status%d want%d", out.Code, want)
			}
			if strings.Contains(out.Body.String(), cookie.Value) || strings.Contains(out.Body.String(), testAccessKey) || strings.Contains(out.Body.String(), "token_sha256") {
				t.Fatal("session response leaks secrets")
			}
		})
	}
}

func TestLoginIsStrictAndOriginRestricted(t *testing.T) {
	r, _ := testRegistry(t)
	for _, tc := range []struct {
		name, body, origin string
		want               int
	}{
		{"missing-origin", `{"access_key":"` + testAccessKey + `"}`, "", 403},
		{"foreign-origin", `{}`, "https://example.com", 403},
		{"unlisted-local-port", `{}`, "http://127.0.0.1:9999", 403},
		{"origin-userinfo", `{}`, "http://x@127.0.0.1:5173", 403},
		{"client-role", `{"access_key":"` + testAccessKey + `","role":"coordinator"}`, "http://127.0.0.1:5173", 400},
		{"wrong-field-case", `{"ACCESS_KEY":"` + testAccessKey + `"}`, "http://127.0.0.1:5173", 400},
		{"duplicate-key", `{"access_key":"a","access_key":"b"}`, "http://127.0.0.1:5173", 400},
		{"trailing-object", `{"access_key":"a"}{}`, "http://127.0.0.1:5173", 400},
		{"nonstring-key", `{"access_key":true}`, "http://127.0.0.1:5173", 400},
		{"too-large", `{"access_key":"` + strings.Repeat("x", 1024) + `"}`, "http://127.0.0.1:5173", 413},
		{"wrong-key", `{"access_key":"not-valid"}`, "http://127.0.0.1:5173", 401},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := request(r.SessionHandler(), "POST", "/api/v1/session", tc.body, tc.origin, nil)
			if out.Code != tc.want {
				t.Fatalf("status%d want%d", out.Code, tc.want)
			}
			if strings.Contains(out.Body.String(), testAccessKey) {
				t.Fatal("error leaked key")
			}
		})
	}
	for _, body := range []string{"{}", ""} {
		req := httptest.NewRequest("POST", "/api/v1/session", strings.NewReader(body))
		req.Header.Set("Origin", "http://127.0.0.1:5173")
		out := httptest.NewRecorder()
		r.SessionHandler().ServeHTTP(out, req)
		if out.Code != 415 {
			t.Fatal("missing JSON content type accepted")
		}
	}
	if out := request(r.SessionHandler(), "PUT", "/api/v1/session", "", "", nil); out.Code != 405 {
		t.Fatal("method not restricted")
	}
}

func TestCookieMutationsRequireOriginAndBearerDoesNotBypassBadOrigin(t *testing.T) {
	r, _ := testRegistry(t)
	cookie := login(t, r)
	handler := r.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) }))
	for _, origin := range []string{"", "http://127.0.0.1:9999", "https://example.com"} {
		if out := request(handler, "POST", "/api/test", "", origin, cookie); out.Code != 403 {
			t.Fatal("cookie mutation without allowed Origin")
		}
	}
	if out := request(handler, "POST", "/api/test", "", "http://127.0.0.1:5173", cookie); out.Code != 204 {
		t.Fatal("allowed cookie mutation rejected")
	}
	if out := request(r.SessionHandler(), "DELETE", "/api/v1/session", "", "", cookie); out.Code != 403 {
		t.Fatal("logout Origin not required")
	}
	req := httptest.NewRequest("POST", "/api/test", nil)
	req.Header.Set("Authorization", "Bearer "+testAccessKey)
	req.Header.Set("Origin", "https://example.com")
	out := httptest.NewRecorder()
	handler.ServeHTTP(out, req)
	if out.Code != 403 {
		t.Fatal("foreign bearer browser Origin accepted")
	}
	req = httptest.NewRequest("POST", "/api/test", nil)
	req.Header.Add("Authorization", "Bearer "+testAccessKey)
	req.Header.Add("Authorization", "Bearer "+testAccessKey)
	out = httptest.NewRecorder()
	handler.ServeHTTP(out, req)
	if out.Code != 401 {
		t.Fatal("ambiguous bearer accepted")
	}
	req = httptest.NewRequest("GET", "/api/test", nil)
	req.AddCookie(cookie)
	req.AddCookie(cookie)
	out = httptest.NewRecorder()
	handler.ServeHTTP(out, req)
	if out.Code != 401 {
		t.Fatal("duplicate session cookie accepted")
	}
}

func TestSessionDoesNotOutliveIdentityAndRotationRevokesPrevious(t *testing.T) {
	r, now := testRegistry(t)
	u := r.byID["coordinator.demo"]
	u.ValidUntil = now.Add(10 * time.Minute)
	r.byID[u.ID] = u
	first := login(t, r)
	if first.MaxAge != 600 {
		t.Fatal("session outlives key")
	}
	body := `{"access_key":"` + testAccessKey + `"}`
	out := request(r.SessionHandler(), "POST", "/api/v1/session", body, "http://127.0.0.1:5173", first)
	if out.Code != 200 {
		t.Fatal("rotation failed")
	}
	if got := request(r.SessionHandler(), "GET", "/api/v1/session", "", "", first); got.Code != 401 {
		t.Fatal("old rotated cookie still valid")
	}
}

func TestConcurrentSessionStorage(t *testing.T) {
	r, _ := testRegistry(t)
	var group sync.WaitGroup
	for i := 0; i < 24; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			out := request(r.SessionHandler(), "POST", "/api/v1/session", `{"access_key":"`+testAccessKey+`"}`, "http://127.0.0.1:5173", nil)
			if out.Code != 200 {
				t.Error("concurrent login failed")
				return
			}
			cookie := out.Result().Cookies()[0]
			if got := request(r.SessionHandler(), "GET", "/api/v1/session", "", "", cookie); got.Code != 200 {
				t.Error("concurrent read failed")
			}
			if got := request(r.SessionHandler(), "DELETE", "/api/v1/session", "", "http://127.0.0.1:5173", cookie); got.Code != 204 {
				t.Error("concurrent logout failed")
			}
		}()
	}
	group.Wait()
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.sessions) != 0 {
		t.Fatal("revoked sessions retained")
	}
}

func TestDuplicateCredentialHashRejected(t *testing.T) {
	data, _ := testConfig(t)
	var cfg privateConfig
	json.NewDecoder(bytes.NewReader(data)).Decode(&cfg)
	other := cfg.Users[0]
	other.ID = "other.demo"
	cfg.Users = append(cfg.Users, other)
	data, _ = json.Marshal(cfg)
	path := filepath.Join(t.TempDir(), "duplicate.json")
	os.WriteFile(path, data, 0600)
	if _, err := Load(path); err == nil {
		t.Fatal("duplicate key hash accepted")
	}
}
