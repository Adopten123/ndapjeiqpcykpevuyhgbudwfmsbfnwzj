// Package demoauth задаёт доверенную личность для изолированного учебного контура.
// Предметные полномочия проверяются доменным сервисом, а не этим пакетом.
package demoauth

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"rzdarctica.local/backend/internal/core/config"
)

const CookieName = "rzd_demo_session"
const Scope = "demo-chain"

type User struct {
	ID             string    `json:"id"`
	Name           string    `json:"name"`
	Role           string    `json:"role"`
	OrganizationID string    `json:"organization_id"`
	Subjects       []string  `json:"subjects"`
	ValidFrom      time.Time `json:"valid_from"`
	ValidUntil     time.Time `json:"valid_until"`
}

type credential struct {
	User
	TokenSHA256 string `json:"token_sha256"`
}

type privateConfig struct {
	Scope string       `json:"scope"`
	Users []credential `json:"users"`
}

type session struct {
	userID    string
	expiresAt time.Time
}

type Registry struct {
	users    []User
	byID     map[string]User
	keys     map[[32]byte]string
	mu       sync.Mutex
	sessions map[[32]byte]session
	now      func() time.Time
}

type contextKey struct{}

func cloneUser(u User) User {
	u.Subjects = append([]string{}, u.Subjects...)
	return u
}

func UserFromContext(ctx context.Context) (User, bool) {
	u, ok := ctx.Value(contextKey{}).(User)
	return cloneUser(u), ok
}

func (r *Registry) Users() []User {
	result := make([]User, 0, len(r.users))
	for _, u := range r.users {
		result = append(result, cloneUser(u))
	}
	return result
}

// strictJSON запрещает неизвестные и повторяющиеся поля на любом уровне.
func strictJSON(data []byte, dst any) error {
	if !utf8.Valid(data) {
		return errors.New("JSON must be UTF-8")
	}
	check := json.NewDecoder(bytes.NewReader(data))
	var value func() error
	value = func() error {
		token, err := check.Token()
		if err != nil {
			return err
		}
		if delim, ok := token.(json.Delim); ok {
			switch delim {
			case '{':
				seen := map[string]bool{}
				for check.More() {
					key, err := check.Token()
					name, ok := key.(string)
					if err != nil || !ok || seen[name] {
						return errors.New("ambiguous JSON")
					}
					seen[name] = true
					if err := value(); err != nil {
						return err
					}
				}
			case '[':
				for check.More() {
					if err := value(); err != nil {
						return err
					}
				}
			default:
				return errors.New("invalid JSON")
			}
			_, err = check.Token()
			return err
		}
		return nil
	}
	if err := value(); err != nil {
		return err
	}
	if _, err := check.Token(); err != io.EOF {
		return errors.New("trailing JSON")
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	return d.Decode(dst)
}

func exactFields(data []byte, names ...string) bool {
	var fields map[string]json.RawMessage
	if json.Unmarshal(data, &fields) != nil || len(fields) != len(names) {
		return false
	}
	for _, name := range names {
		if _, ok := fields[name]; !ok {
			return false
		}
	}
	return true
}

var idPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

func Load(path string) (*Registry, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("требуется файл локальной аутентификации")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, errors.New("не удалось прочитать файл локальной аутентификации")
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil || !stat.Mode().IsRegular() {
		return nil, errors.New("нужен обычный файл локальной аутентификации")
	}
	data, err := io.ReadAll(io.LimitReader(f, 1024*1024+1))
	if err != nil || len(data) > 1024*1024 {
		return nil, errors.New("неверный размер файла аутентификации")
	}
	var cfg privateConfig
	if strictJSON(data, &cfg) != nil || !exactFields(data, "scope", "users") || cfg.Scope != Scope || len(cfg.Users) == 0 || len(cfg.Users) > 100 {
		return nil, errors.New("неверная закрытая структура файла аутентификации")
	}
	var rawConfig struct {
		Users []json.RawMessage `json:"users"`
	}
	if json.Unmarshal(data, &rawConfig) != nil {
		return nil, errors.New("неверная закрытая структура файла аутентификации")
	}
	for _, rawUser := range rawConfig.Users {
		if !exactFields(rawUser, "id", "name", "role", "organization_id", "subjects", "valid_from", "valid_until", "token_sha256") {
			return nil, errors.New("неверный состав полей учебной учётной записи")
		}
	}
	r := &Registry{byID: map[string]User{}, keys: map[[32]byte]string{}, sessions: map[[32]byte]session{}, now: time.Now}
	for _, entry := range cfg.Users {
		u := entry.User
		if !idPattern.MatchString(u.ID) || strings.TrimSpace(u.Name) == "" || len(u.Name) > 300 || !idPattern.MatchString(u.OrganizationID) || u.ValidFrom.IsZero() || u.ValidUntil.IsZero() || !u.ValidFrom.Before(u.ValidUntil) || len(u.Subjects) == 0 {
			return nil, errors.New("неверные атрибуты учебной учётной записи")
		}
		if u.Role != "coordinator" && u.Role != "auditor" && u.Role != "participant" {
			return nil, errors.New("неподдерживаемая учебная роль")
		}
		seen := map[string]bool{}
		for _, subject := range u.Subjects {
			if seen[subject] || subject != "*" && !idPattern.MatchString(subject) || subject == "*" && u.Role == "participant" {
				return nil, errors.New("неверная область учебной учётной записи")
			}
			seen[subject] = true
		}
		if _, exists := r.byID[u.ID]; exists {
			return nil, errors.New("повтор учебного субъекта")
		}
		hash, err := hex.DecodeString(entry.TokenSHA256)
		if err != nil || len(hash) != 32 || len(entry.TokenSHA256) != 64 {
			return nil, errors.New("неверный отпечаток учебного ключа")
		}
		var key [32]byte
		copy(key[:], hash)
		if _, exists := r.keys[key]; exists {
			return nil, errors.New("повтор отпечатка учебного ключа")
		}
		r.keys[key], r.byID[u.ID] = u.ID, cloneUser(u)
		r.users = append(r.users, cloneUser(u))
	}
	return r, nil
}

func active(u User, now time.Time) bool { return !now.Before(u.ValidFrom) && now.Before(u.ValidUntil) }

func (r *Registry) keyUser(key string, now time.Time) (User, bool) {
	if len(key) == 0 || len(key) > 256 {
		return User{}, false
	}
	id, ok := r.keys[sha256.Sum256([]byte(key))]
	u := r.byID[id]
	return cloneUser(u), ok && active(u, now)
}

func cookieToken(req *http.Request) (string, bool) {
	var token string
	count := 0
	for _, cookie := range req.Cookies() {
		if cookie.Name == CookieName {
			token, count = cookie.Value, count+1
		}
	}
	return token, count == 1 && token != "" && len(token) <= 256
}

func (r *Registry) authenticate(req *http.Request) (User, bool, bool) {
	now := r.now()
	headers := req.Header.Values("Authorization")
	if len(headers) != 0 {
		if len(headers) != 1 {
			return User{}, false, false
		}
		parts := strings.Fields(headers[0])
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
			return User{}, false, false
		}
		u, ok := r.keyUser(parts[1], now)
		return u, ok, false
	}
	token, ok := cookieToken(req)
	if !ok {
		return User{}, false, true
	}
	hash := sha256.Sum256([]byte(token))
	r.mu.Lock()
	s, ok := r.sessions[hash]
	if ok && !now.Before(s.expiresAt) {
		delete(r.sessions, hash)
		ok = false
	}
	r.mu.Unlock()
	u := r.byID[s.userID]
	return cloneUser(u), ok && active(u, now), true
}

func allowedOrigin(origin string) bool {
	return config.AllowedUIOrigin(origin)
}

func write(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	if value != nil {
		_ = json.NewEncoder(w).Encode(value)
	}
}

func fail(w http.ResponseWriter, status int, code, message string) {
	write(w, status, map[string]any{"error": map[string]string{"code": code, "message": message}})
}

func (r *Registry) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		u, ok, cookie := r.authenticate(req)
		if !ok {
			fail(w, 401, "authentication_required", "Войдите с действующим учебным ключом.")
			return
		}
		mutation := req.Method != http.MethodGet && req.Method != http.MethodHead && req.Method != http.MethodOptions
		origin := req.Header.Get("Origin")
		if origin != "" && !allowedOrigin(origin) || cookie && mutation && !allowedOrigin(origin) {
			fail(w, 403, "origin_forbidden", "Источник запроса не разрешён для учебного контура.")
			return
		}
		next.ServeHTTP(w, req.WithContext(context.WithValue(req.Context(), contextKey{}, cloneUser(u))))
	})
}

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
