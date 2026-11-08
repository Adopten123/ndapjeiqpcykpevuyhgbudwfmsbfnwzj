package httptransport

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"regexp"
	"time"

	"rzdarctica.local/backend/internal/features/calculations"
)

type Service interface {
	Create(context.Context, string, string) (calculations.Calculation, error)
	Get(context.Context, string) (calculations.Calculation, error)
	List(context.Context) ([]calculations.Calculation, error)
	Scenarios(context.Context) ([]calculations.Scenario, error)
}

type Health func(context.Context) (bool, bool)
type Handler struct {
	service Service
	health  Health
}

var keyPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)
var idPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func New(service Service, health Health) http.Handler {
	h := &Handler{service: service, health: health}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", h.Health)
	mux.HandleFunc("GET /api/v1/scenarios", h.Scenarios)
	mux.HandleFunc("POST /api/v1/calculations", h.Create)
	mux.HandleFunc("GET /api/v1/calculations", h.List)
	mux.HandleFunc("GET /api/v1/calculations/{id}", h.Get)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		mux.ServeHTTP(w, r)
	})
}

func write(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func fail(w http.ResponseWriter, status int, code, message string) {
	write(w, status, map[string]any{"error": map[string]string{"code": code, "message": message}})
}

func handleError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, calculations.ErrNotFound):
		fail(w, 404, "not_found", "Расчёт не найден.")
	case errors.Is(err, calculations.ErrConflict):
		fail(w, 409, "idempotency_conflict", "Этот ключ уже использован для другого сценария.")
	case errors.Is(err, calculations.ErrCompute):
		fail(w, 503, "compute_unavailable", "Расчётный сервис временно недоступен.")
	case errors.Is(err, calculations.ErrInvalid):
		fail(w, 400, "invalid_request", "Некорректный сценарий расчёта.")
	default:
		fail(w, 500, "internal_error", "Не удалось обработать запрос.")
	}
}

func (h *Handler) Health(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	db, compute := h.health(ctx)
	status, code := "ok", 200
	if !db || !compute {
		status, code = "degraded", 503
	}
	write(w, code, map[string]any{"status": status, "mode": "demo", "database": db, "compute": compute})
}

func (h *Handler) Scenarios(w http.ResponseWriter, r *http.Request) {
	values, err := h.service.Scenarios(r.Context())
	if err != nil {
		handleError(w, calculations.ErrCompute)
		return
	}
	write(w, 200, values)
}

func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	if origin := r.Header.Get("Origin"); origin != "" {
		u, err := url.Parse(origin)
		valid := err == nil && u.Scheme == "http" && (u.Hostname() == "127.0.0.1" || u.Hostname() == "localhost" || u.Hostname() == "::1") && u.User == nil && u.Path == "" && u.RawQuery == "" && u.Fragment == ""
		if !valid {
			fail(w, 403, "origin_forbidden", "Источник запроса не разрешён для локального демо.")
			return
		}
	}
	typeName, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || typeName != "application/json" {
		fail(w, 415, "json_required", "Ожидается application/json.")
		return
	}
	key := r.Header.Get("Idempotency-Key")
	if !keyPattern.MatchString(key) {
		fail(w, 400, "idempotency_key_required", "Нужен корректный Idempotency-Key длиной до128 символов.")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1024)
	data, err := io.ReadAll(r.Body)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			fail(w, 413, "body_too_large", "Слишком большой запрос.")
		} else {
			fail(w, 400, "invalid_request", "Нужен JSON только с полем scenario_id.")
		}
		return
	}
	scenario, err := decodeScenario(data)
	if err != nil || !calculations.ValidScenario(scenario) {
		fail(w, 400, "invalid_scenario", "Допустимы сценарии S0–S3 и один JSON-объект.")
		return
	}
	value, err := h.service.Create(r.Context(), key, scenario)
	if err != nil {
		handleError(w, err)
		return
	}
	w.Header().Set("Location", "/api/v1/calculations/"+value.ID)
	write(w, 202, value)
}

func decodeScenario(data []byte) (string, error) {
	d := json.NewDecoder(bytes.NewReader(data))
	open, err := d.Token()
	if err != nil || open != json.Delim('{') {
		return "", errors.New("object required")
	}
	seen := false
	var scenario string
	for d.More() {
		key, err := d.Token()
		if err != nil || key != "scenario_id" || seen {
			return "", errors.New("unknown or duplicate field")
		}
		seen = true
		if err := d.Decode(&scenario); err != nil {
			return "", err
		}
	}
	close, err := d.Token()
	if err != nil || close != json.Delim('}') {
		return "", errors.New("invalid object")
	}
	if err := d.Decode(&struct{}{}); err != io.EOF {
		return "", errors.New("trailing content")
	}
	return scenario, nil
}

func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !idPattern.MatchString(id) {
		fail(w, 400, "invalid_id", "Некорректный идентификатор расчёта.")
		return
	}
	value, err := h.service.Get(r.Context(), id)
	if err != nil {
		handleError(w, err)
		return
	}
	write(w, 200, value)
}

func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	values, err := h.service.List(r.Context())
	if err != nil {
		handleError(w, err)
		return
	}
	write(w, 200, values)
}
