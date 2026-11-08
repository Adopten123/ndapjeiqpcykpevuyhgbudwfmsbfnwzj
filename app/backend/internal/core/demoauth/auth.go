package demoauth

import (
	"context"
	"crypto/sha256"
	"net/http"
	"strings"
	"sync"
	"time"
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
