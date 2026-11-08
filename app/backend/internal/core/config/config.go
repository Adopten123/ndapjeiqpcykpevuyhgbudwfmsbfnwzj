package config

import (
	"errors"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
)

type Config struct {
	DatabaseURL  string
	ComputeURL   string
	SimulatorURL string
	HTTPAddr     string
}

func LoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func IsolatedDatabase(host, name string) bool {
	return LoopbackHost(host) && strings.HasPrefix(name, "rzd_arctica_") && !strings.ContainsAny(name, "/\\")
}

// ValidUIOrigin accepts one explicit loopback HTTP origin, never a pattern.
func ValidUIOrigin(value string) bool {
	u, err := url.Parse(value)
	if err != nil || u.Scheme != "http" || !LoopbackHost(u.Hostname()) || u.User != nil || u.Path != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" {
		return false
	}
	port, err := strconv.Atoi(u.Port())
	return err == nil && port > 0 && port <= 65535 && value == "http://"+u.Host
}

// AllowedUIOrigin preserves the original demo origins and optionally permits
// the single local frontend configured for an isolated QA instance.
func AllowedUIOrigin(origin string) bool {
	switch origin {
	case "http://127.0.0.1:5173", "http://127.0.0.1:8090", "http://localhost:5173", "http://localhost:8090":
		return true
	}
	configured := os.Getenv("RZD_UI_ORIGIN")
	return configured != "" && origin == configured && ValidUIOrigin(configured)
}

func Load() (Config, error) {
	if origin := os.Getenv("RZD_UI_ORIGIN"); origin != "" && !ValidUIOrigin(origin) {
		return Config{}, errors.New("RZD_UI_ORIGIN должен быть одним полным loopback HTTP Origin с явным портом")
	}
	c := Config{DatabaseURL: os.Getenv("RZD_DATABASE_URL"), ComputeURL: os.Getenv("RZD_COMPUTE_URL"), SimulatorURL: os.Getenv("RZD_SIMULATOR_URL"), HTTPAddr: os.Getenv("RZD_HTTP_ADDR")}
	if c.ComputeURL == "" {
		c.ComputeURL = "http://127.0.0.1:8091"
	}
	if c.SimulatorURL == "" {
		c.SimulatorURL = "http://127.0.0.1:8094"
	}
	if c.HTTPAddr == "" {
		c.HTTPAddr = "127.0.0.1:8090"
	}
	db, err := url.Parse(c.DatabaseURL)
	if err != nil || db.Scheme != "postgres" && db.Scheme != "postgresql" || !LoopbackHost(db.Hostname()) {
		return Config{}, errors.New("RZD_DATABASE_URL должен указывать на отдельную локальную PostgreSQL")
	}
	if !IsolatedDatabase(db.Hostname(), strings.TrimPrefix(db.Path, "/")) {
		return Config{}, errors.New("имя учебной базы должно начинаться с rzd_arctica_")
	}
	for _, endpoint := range []struct{ name, value string }{{"RZD_COMPUTE_URL", c.ComputeURL}, {"RZD_SIMULATOR_URL", c.SimulatorURL}} {
		u, err := url.Parse(endpoint.value)
		if err != nil || u.Scheme != "http" || !LoopbackHost(u.Hostname()) || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Path != "" && u.Path != "/" {
			return Config{}, errors.New(endpoint.name + " должен быть локальным HTTP-адресом без дополнительных компонентов")
		}
	}
	host, _, err := net.SplitHostPort(c.HTTPAddr)
	if err != nil || !LoopbackHost(host) {
		return Config{}, errors.New("учебный сервер может слушать только loopback")
	}
	return c, nil
}
