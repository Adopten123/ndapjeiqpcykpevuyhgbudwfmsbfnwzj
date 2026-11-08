package config

import "testing"

func TestDemoConfigurationBoundary(t *testing.T) {
	t.Setenv("RZD_UI_ORIGIN", "")
	t.Setenv("RZD_DATABASE_URL", "postgres://USER:PLACEHOLDER@127.0.0.1:55432/rzd_arctica_demo?sslmode=disable")
	t.Setenv("RZD_COMPUTE_URL", "http://127.0.0.1:8091")
	t.Setenv("RZD_HTTP_ADDR", "127.0.0.1:8090")
	if _, err := Load(); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, key, value string }{
		{"remote-db", "RZD_DATABASE_URL", "postgres://USER:PLACEHOLDER@example.com/rzd_arctica_demo"},
		{"old-local-db", "RZD_DATABASE_URL", "postgres://USER:PLACEHOLDER@127.0.0.1/postgres"},
		{"remote-compute", "RZD_COMPUTE_URL", "http://example.com:8091"},
		{"compute-userinfo", "RZD_COMPUTE_URL", "http://USER:PLACEHOLDER@127.0.0.1:8091"},
		{"public-bind", "RZD_HTTP_ADDR", "0.0.0.0:8090"},
		{"foreign-ui", "RZD_UI_ORIGIN", "http://example.com:5183"},
		{"wildcard-ui", "RZD_UI_ORIGIN", "*"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(tc.key, tc.value)
			if _, err := Load(); err == nil {
				t.Fatal("unsafe configuration accepted")
			}
		})
	}
}

func TestExplicitLoopbackUIOrigin(t *testing.T) {
	t.Setenv("RZD_UI_ORIGIN", "http://127.0.0.1:5183")
	for _, origin := range []string{"http://127.0.0.1:5183", "http://127.0.0.1:5173", "http://localhost:8090"} {
		if !AllowedUIOrigin(origin) {
			t.Errorf("expected configured or original origin: %q", origin)
		}
	}
	for _, origin := range []string{"", "*", "null", "http://localhost:5183", "http://127.0.0.1:5184", "https://example.com", "http://127.0.0.1:5183/", "http://x@127.0.0.1:5183", "http://127.0.0.1:5183?", "http://127.0.0.1:5183#x"} {
		if AllowedUIOrigin(origin) {
			t.Errorf("unconfigured origin accepted: %q", origin)
		}
	}
	for _, origin := range []string{"*", "null", "https://127.0.0.1:5183", "http://example.com:5183", "http://0.0.0.0:5183", "http://127.0.0.1", "http://127.0.0.1:0", "http://127.0.0.1:65536", "http://127.0.0.1:5183/", "http://x@127.0.0.1:5183", "http://127.0.0.1:5183?", "http://127.0.0.1:5183#"} {
		t.Run(origin, func(t *testing.T) {
			t.Setenv("RZD_UI_ORIGIN", origin)
			if ValidUIOrigin(origin) || AllowedUIOrigin(origin) {
				t.Fatal("unsafe extra origin accepted")
			}
		})
	}
	t.Setenv("RZD_UI_ORIGIN", "")
	if AllowedUIOrigin("http://127.0.0.1:5183") {
		t.Fatal("test port enabled by default")
	}
}
