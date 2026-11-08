package demoauth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"io"
	"mime"
	"net/http"
	"time"
)

func clearCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: CookieName, Value: "", Path: "/api", HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: -1, Expires: time.Unix(1, 0).UTC()})
}

func (r *Registry) SessionHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path != "/api/v1/session" {
			fail(w, 404, "not_found", "Операция не найдена.")
			return
		}
		if req.Method == http.MethodGet {
			u, ok, _ := r.authenticate(req)
			if !ok {
				fail(w, 401, "authentication_required", "Учебная сессия отсутствует или истекла.")
				return
			}
			write(w, 200, map[string]any{"user": u})
			return
		}
		if req.Method != http.MethodPost && req.Method != http.MethodDelete {
			w.Header().Set("Allow", "GET, POST, DELETE")
			fail(w, 405, "method_not_allowed", "Метод не поддерживается.")
			return
		}
		if !allowedOrigin(req.Header.Get("Origin")) {
			fail(w, 403, "origin_forbidden", "Для входа и выхода нужен разрешённый локальный источник.")
			return
		}
		if req.Method == http.MethodDelete {
			if token, ok := cookieToken(req); ok {
				r.mu.Lock()
				delete(r.sessions, sha256.Sum256([]byte(token)))
				r.mu.Unlock()
			}
			clearCookie(w)
			write(w, 204, nil)
			return
		}
		media, _, err := mime.ParseMediaType(req.Header.Get("Content-Type"))
		if err != nil || media != "application/json" || req.Header.Get("Content-Encoding") != "" && req.Header.Get("Content-Encoding") != "identity" {
			fail(w, 415, "json_required", "Нужен несжатый application/json.")
			return
		}
		data, err := io.ReadAll(http.MaxBytesReader(w, req.Body, 1024))
		if err != nil {
			var max *http.MaxBytesError
			if errors.As(err, &max) {
				fail(w, 413, "body_too_large", "Слишком большой запрос входа.")
			} else {
				fail(w, 400, "invalid_request", "Не удалось прочитать запрос входа.")
			}
			return
		}
		var body struct {
			AccessKey string `json:"access_key"`
		}
		if strictJSON(data, &body) != nil || !exactFields(data, "access_key") || body.AccessKey == "" {
			fail(w, 400, "invalid_request", "Нужен JSON только с учебным ключом.")
			return
		}
		now := r.now()
		u, ok := r.keyUser(body.AccessKey, now)
		if !ok {
			fail(w, 401, "authentication_required", "Учебный ключ недействителен или истёк.")
			return
		}
		var raw [32]byte
		if _, err := rand.Read(raw[:]); err != nil {
			fail(w, 500, "session_failed", "Не удалось создать учебную сессию.")
			return
		}
		token := base64.RawURLEncoding.EncodeToString(raw[:])
		expires := now.Add(time.Hour)
		if u.ValidUntil.Before(expires) {
			expires = u.ValidUntil
		}
		r.mu.Lock()
		for key, s := range r.sessions {
			if !now.Before(s.expiresAt) {
				delete(r.sessions, key)
			}
		}
		if len(r.sessions) >= 4096 {
			r.mu.Unlock()
			fail(w, 503, "session_capacity", "Учебный сервер временно не может создать сессию.")
			return
		}
		if previous, ok := cookieToken(req); ok {
			delete(r.sessions, sha256.Sum256([]byte(previous)))
		}
		r.sessions[sha256.Sum256([]byte(token))] = session{userID: u.ID, expiresAt: expires}
		r.mu.Unlock()
		age := int(expires.Sub(now).Seconds())
		if age < 1 {
			age = 1
		}
		http.SetCookie(w, &http.Cookie{Name: CookieName, Value: token, Path: "/api", HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: age, Expires: expires.UTC()})
		write(w, 200, map[string]any{"user": u})
	})
}
