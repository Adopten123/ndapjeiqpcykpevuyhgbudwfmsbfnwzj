package simulation

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"rzdarctica.local/backend/internal/core/demoauth"
)

const route = "/api/v1/simulation/runs"

type problem struct {
	Error string `json:"error"`
	Field string `json:"field,omitempty"`
}

func respond(w http.ResponseWriter, code int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(body)
}

func failure(w http.ResponseWriter, err error) {
	var field *fieldError
	switch {
	case errors.As(err, &field):
		respond(w, http.StatusBadRequest, problem{err.Error(), field.field})
	case errors.Is(err, errMissing):
		respond(w, http.StatusNotFound, problem{Error: err.Error()})
	case errors.Is(err, errStale), errors.Is(err, errBlocked):
		respond(w, http.StatusConflict, problem{Error: err.Error()})
	case errors.Is(err, errSeed):
		respond(w, http.StatusServiceUnavailable, problem{Error: err.Error()})
	default:
		respond(w, http.StatusInternalServerError, problem{Error: "Не удалось сохранить или прочитать учебный прогон."})
	}
}

func parse(w http.ResponseWriter, r *http.Request, dst any) bool {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 16*1024))
	if err != nil || strictJSON(bytes.TrimSpace(body), dst) != nil {
		respond(w, http.StatusBadRequest, problem{Error: "Проверьте формат данных запроса."})
		return false
	}
	return true
}

func (s *Store) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+route, func(w http.ResponseWriter, r *http.Request) {
		runs, err := s.list(r.Context())
		if err != nil {
			failure(w, err)
			return
		}
		respond(w, http.StatusOK, map[string]any{"runs": runs})
	})
	mux.HandleFunc("POST "+route, func(w http.ResponseWriter, r *http.Request) {
		var req createRequest
		if !parse(w, r, &req) {
			return
		}
		actor, _ := demoauth.UserFromContext(r.Context())
		value, err := s.create(r.Context(), req, actor.ID)
		if err != nil {
			failure(w, err)
			return
		}
		respond(w, http.StatusCreated, value)
	})
	mux.HandleFunc("GET "+route+"/{id}", func(w http.ResponseWriter, r *http.Request) {
		value, err := get(r.Context(), s.pool, r.PathValue("id"), false)
		replyRun(w, value.run, err)
	})
	for action, change := range map[string]func(context.Context, string, int) (run, error){"step": s.step, "retry": s.retry} {
		mux.HandleFunc("POST "+route+"/{id}/"+action, func(w http.ResponseWriter, r *http.Request) {
			var req struct {
				Version *int `json:"state_version"`
			}
			if !parse(w, r, &req) {
				return
			}
			if req.Version == nil || *req.Version < 0 {
				failure(w, &fieldError{"state_version", "Укажите текущую версию состояния прогона."})
				return
			}
			value, err := change(r.Context(), r.PathValue("id"), *req.Version)
			replyRun(w, value, err)
		})
	}
	mux.HandleFunc("POST "+route+"/{id}/apply", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID string `json:"candidate_id"`
		}
		if !parse(w, r, &req) {
			return
		}
		if !uuidPattern.MatchString(req.ID) {
			failure(w, &fieldError{"candidate_id", "Укажите идентификатор проверенного кандидата."})
			return
		}
		actor, _ := demoauth.UserFromContext(r.Context())
		value, err := s.apply(r.Context(), r.PathValue("id"), req.ID, actor.ID)
		replyRun(w, value, err)
	})
	edit := func(w http.ResponseWriter, r *http.Request) {
		var req eventInput
		if r.Method != http.MethodDelete && !parse(w, r, &req) {
			return
		}
		value, err := s.manual(r.Context(), r.PathValue("id"), r.PathValue("event"), r.Method, req)
		replyRun(w, value, err)
	}
	mux.HandleFunc("POST "+route+"/{id}/events", edit)
	mux.HandleFunc("PUT "+route+"/{id}/events/{event}", edit)
	mux.HandleFunc("DELETE "+route+"/{id}/events/{event}", edit)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 22*time.Second)
		defer cancel()
		mux.ServeHTTP(w, r.WithContext(ctx))
	})
}

func replyRun(w http.ResponseWriter, value run, err error) {
	if err != nil {
		failure(w, err)
		return
	}
	respond(w, http.StatusOK, value)
}
