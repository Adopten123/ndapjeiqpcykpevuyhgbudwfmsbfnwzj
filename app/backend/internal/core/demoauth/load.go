package demoauth

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"regexp"
	"strings"
	"time"
)

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
