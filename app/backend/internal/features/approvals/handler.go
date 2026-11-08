package approvals

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"regexp"
	"time"
	"unicode/utf8"

	"rzdarctica.local/backend/internal/core/config"
	"rzdarctica.local/backend/internal/core/demoauth"
	"rzdarctica.local/backend/internal/core/jsonutil"
)

var uuidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
var keyPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)

func reply(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
func failure(w http.ResponseWriter, err error) {
	status, code, msg := 500, "internal_error", "Не удалось обработать запрос согласования."
	switch {
	case errors.Is(err, ErrSourceUnavailable):
		status, code, msg = 503, "source_unavailable", "Для текущей редакции нет сохранённого учебного снимка. Требуется явный источник или новый изолированный контур."
	case errors.Is(err, ErrSourceMutationRequired):
		status, code, msg = 409, "deprecated_source_mutation", "Для новой редакции укажите изменённые условия рейса A; простое повышение номера без данных запрещено."
	case errors.Is(err, ErrInvalid):
		status, code, msg = 400, "invalid_request", "Некорректный запрос согласования."
	case errors.Is(err, ErrForbidden):
		status, code, msg = 403, "forbidden", "Действие недоступно для текущего назначения."
	case errors.Is(err, ErrNotFound):
		status, code, msg = 404, "not_found", "Пакет не найден или недоступен."
	case errors.Is(err, ErrConflict):
		status, code, msg = 409, "conflict", "Версия, ответы или условия изменились. Обновите пакет; проверьте сроки ответов и действующие назначения."
	case errors.Is(err, ErrUnsupported):
		status, code, msg = 422, "unsupported_calculation", "Для согласования поддержан только завершённый OPTIMAL-расчёт учебного S1."
	case errors.Is(err, ErrCycleClosed):
		status, code, msg = 409, "cycle_closed", "Учебный цикл S1 завершён или его исходные условия изменились. Новый цикл требует отдельного снимка и расчётной базы; этот профиль их не создаёт."
	}
	reply(w, status, map[string]any{"error": map[string]string{"code": code, "message": msg}})
}
func originAllowed(value string) bool {
	if value == "" {
		return true
	}
	return config.AllowedUIOrigin(value)
}
func parse(w http.ResponseWriter, r *http.Request, target any) bool {
	if !originAllowed(r.Header.Get("Origin")) {
		failure(w, ErrForbidden)
		return false
	}
	typeName, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || typeName != "application/json" {
		reply(w, 415, map[string]any{"error": map[string]string{"code": "json_required", "message": "Ожидается application/json."}})
		return false
	}
	if !keyPattern.MatchString(r.Header.Get("Idempotency-Key")) {
		failure(w, ErrInvalid)
		return false
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4096))
	if err != nil {
		var large *http.MaxBytesError
		if errors.As(err, &large) {
			reply(w, 413, map[string]any{"error": map[string]string{"code": "body_too_large", "message": "Слишком большой запрос."}})
		} else {
			failure(w, ErrInvalid)
		}
		return false
	}
	if !requiredRequestFields(data, target) || !jsonutil.UniqueKeys(data) {
		failure(w, ErrInvalid)
		return false
	}
	if !utf8.Valid(data) || !json.Valid(data) {
		failure(w, ErrInvalid)
		return false
	}
	d := json.NewDecoder(bytes.NewReader(data))
	open, err := d.Token()
	if err != nil || open != json.Delim('{') {
		failure(w, ErrInvalid)
		return false
	}
	seen := map[string]bool{}
	for d.More() {
		name, err := d.Token()
		if err != nil {
			failure(w, ErrInvalid)
			return false
		}
		key, ok := name.(string)
		if !ok || seen[key] {
			failure(w, ErrInvalid)
			return false
		}
		seen[key] = true
		var raw json.RawMessage
		if d.Decode(&raw) != nil {
			failure(w, ErrInvalid)
			return false
		}
	}
	if _, err = d.Token(); err != nil {
		failure(w, ErrInvalid)
		return false
	}
	d = json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if d.Decode(target) != nil {
		failure(w, ErrInvalid)
		return false
	}
	return true
}
func actor(r *http.Request) string { u, _ := demoauth.UserFromContext(r.Context()); return u.ID }

func (s *Store) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/planning-context", func(w http.ResponseWriter, r *http.Request) {
		value, err := s.PlanningContext(r.Context(), actor(r))
		if err != nil {
			failure(w, err)
			return
		}
		reply(w, 200, value)
	})
	mux.HandleFunc("POST /api/v1/planning-calculations", func(w http.ResponseWriter, r *http.Request) {
		var req PlanningRequest
		if !parse(w, r, &req) {
			return
		}
		if req.ExpectedSourceRevision < 1 || req.ExpectedAcceptedVersion < 0 {
			failure(w, ErrInvalid)
			return
		}
		value, err := s.PlanningCalculation(r.Context(), actor(r), r.Header.Get("Idempotency-Key"), req)
		if err != nil {
			failure(w, err)
			return
		}
		w.Header().Set("Location", "/api/v1/calculations/"+value.ID)
		reply(w, 202, value)
	})
	mux.HandleFunc("POST /api/v1/planning-context/source", func(w http.ResponseWriter, r *http.Request) {
		var req SourceRequest
		if !parse(w, r, &req) {
			return
		}
		if req.ExpectedSourceRevision < 1 || req.ExpectedAcceptedVersion < 0 || !validWindow(req.Departure, req.LoadWindow) {
			failure(w, ErrInvalid)
			return
		}
		value, err := s.UpdateSource(r.Context(), actor(r), r.Header.Get("Idempotency-Key"), req)
		if err != nil {
			failure(w, err)
			return
		}
		reply(w, 200, value)
	})

	mux.HandleFunc("GET /api/v1/approval-packets", func(w http.ResponseWriter, r *http.Request) {
		value, err := s.List(r.Context(), actor(r))
		if err != nil {
			failure(w, err)
			return
		}
		reply(w, 200, value)
	})
	mux.HandleFunc("GET /api/v1/approval-packets/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if !uuidPattern.MatchString(id) {
			failure(w, ErrInvalid)
			return
		}
		value, err := s.Get(r.Context(), actor(r), id)
		if err != nil {
			failure(w, err)
			return
		}
		reply(w, 200, value)
	})
	mux.HandleFunc("POST /api/v1/approval-packets", func(w http.ResponseWriter, r *http.Request) {
		var req CreateRequest
		if !parse(w, r, &req) {
			return
		}
		if !uuidPattern.MatchString(req.CalculationID) || req.ExpectedSourceRevision < 1 || req.ExpectedAcceptedVersion < 0 {
			failure(w, ErrInvalid)
			return
		}
		value, err := s.Create(r.Context(), actor(r), r.Header.Get("Idempotency-Key"), req)
		if err != nil {
			failure(w, err)
			return
		}
		reply(w, 201, value)
	})
	mux.HandleFunc("POST /api/v1/approval-packets/{id}/responses", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		var req RespondRequest
		if !parse(w, r, &req) {
			return
		}
		if !uuidPattern.MatchString(id) || req.PacketVersion < 1 || req.ExpectedRevision < 0 || req.ExpiresAt.IsZero() {
			failure(w, ErrInvalid)
			return
		}
		value, err := s.Respond(r.Context(), actor(r), r.Header.Get("Idempotency-Key"), id, req)
		if err != nil {
			failure(w, err)
			return
		}
		reply(w, 200, value)
	})
	mux.HandleFunc("POST /api/v1/approval-packets/{id}/accept", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		var req AcceptRequest
		if !parse(w, r, &req) {
			return
		}
		if !uuidPattern.MatchString(id) || req.PacketVersion < 1 || req.ExpectedSourceRevision < 1 || req.ExpectedApprovalRevision < 0 || req.ExpectedAcceptedVersion < 0 {
			failure(w, ErrInvalid)
			return
		}
		value, err := s.Accept(r.Context(), actor(r), r.Header.Get("Idempotency-Key"), id, req)
		if err != nil {
			failure(w, err)
			return
		}
		reply(w, 200, value)
	})
	mux.HandleFunc("POST /api/v1/approval-context/invalidate", func(w http.ResponseWriter, r *http.Request) {
		var req InvalidateRequest
		if !parse(w, r, &req) {
			return
		}
		if req.ExpectedSourceRevision < 1 {
			failure(w, ErrInvalid)
			return
		}
		value, err := s.Invalidate(r.Context(), actor(r), r.Header.Get("Idempotency-Key"), req)
		if err != nil {
			failure(w, err)
			return
		}
		reply(w, 200, value)
	})
	return mux
}

// Guard checks current database assignments for every protected request. Mutating
// approval methods repeat this check while holding the workflow transaction lock.
func (s *Store) Guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, ok := demoauth.UserFromContext(r.Context())
		if !ok {
			failure(w, ErrForbidden)
			return
		}
		ctx := r.Context()
		tx, err := s.pool.Begin(ctx)
		if err != nil {
			failure(w, err)
			return
		}
		defer tx.Rollback(ctx)
		g, err := readGrant(ctx, tx, u.ID)
		if err != nil {
			failure(w, err)
			return
		}
		t, err := currentTime(ctx, tx)
		if err != nil {
			failure(w, err)
			return
		}
		if !validGrant(g, t) {
			failure(w, ErrForbidden)
			return
		}
		path := r.URL.Path
		approval := path == "/api/v1/approval-packets" || len(path) > len("/api/v1/approval-packets/") && path[:len("/api/v1/approval-packets/")] == "/api/v1/approval-packets/" || path == "/api/v1/approval-context/invalidate"
		if !approval {
			if g.User.Role != "coordinator" && g.User.Role != "auditor" {
				failure(w, ErrForbidden)
				return
			}
			if r.Method != http.MethodGet && r.Method != http.MethodHead && g.User.Role != "coordinator" {
				failure(w, ErrForbidden)
				return
			}
		}
		if err = tx.Commit(ctx); err != nil {
			failure(w, err)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// Use short per-request deadlines while preserving room for deterministic
// workflow-lock tests; no fixed transport/decision SLA is implied.
const RequestTimeout = 20 * time.Second

func requiredRequestFields(data []byte, target any) bool {
	var names []string
	switch target.(type) {
	case *PlanningRequest:
		names = []string{"expected_source_revision", "expected_accepted_version"}
	case *SourceRequest:
		names = []string{"expected_source_revision", "expected_accepted_version", "service_a_departure_tick", "service_a_load_window"}
	default:
		return true
	}
	var raw map[string]json.RawMessage
	if json.Unmarshal(data, &raw) != nil {
		return false
	}
	for _, key := range names {
		v, ok := raw[key]
		if !ok || bytes.Equal(bytes.TrimSpace(v), []byte("null")) {
			return false
		}
	}
	if _, ok := target.(*SourceRequest); ok {
		var window map[string]json.RawMessage
		if json.Unmarshal(raw["service_a_load_window"], &window) != nil {
			return false
		}
		for _, k := range []string{"start_tick", "end_tick"} {
			v, ok := window[k]
			if !ok || bytes.Equal(bytes.TrimSpace(v), []byte("null")) {
				return false
			}
		}
	}
	return true
}
