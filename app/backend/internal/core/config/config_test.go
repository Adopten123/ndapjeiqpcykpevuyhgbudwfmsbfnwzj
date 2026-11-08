package config

import "testing"

func TestDemoConfigurationBoundary(t *testing.T) {
	t.Setenv("RZD_DEPLOYMENT_MODE", "")
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

func TestComposeConfigurationIsExplicitAndLimitedToServices(t *testing.T) {
	t.Setenv("RZD_DEPLOYMENT_MODE", "compose")
	t.Setenv("RZD_DATABASE_URL", "postgres://rzd_app:PLACEHOLDER@postgres:5432/rzd_arctica_demo?sslmode=disable")
	t.Setenv("RZD_COMPUTE_URL", "http://compute:8091")
	t.Setenv("RZD_SIMULATOR_URL", "http://simulator:8094")
	t.Setenv("RZD_HTTP_ADDR", "0.0.0.0:8090")
	t.Setenv("RZD_UI_ORIGIN", "http://localhost:8080")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.IsolatedDatabase("postgres", "rzd_arctica_demo") || cfg.AllowedDatabaseHost("example.com") ||
		cfg.IsolatedDatabase("postgres", "postgres") || IsolatedDatabase("postgres", "rzd_arctica_demo") {
		t.Fatal("compose database boundary was widened beyond this instance")
	}
	for _, tc := range []struct{ key, value string }{
		{"RZD_DEPLOYMENT_MODE", "anything"},
		{"RZD_DATABASE_URL", "postgres://rzd_app:PLACEHOLDER@example.com:5432/rzd_arctica_demo"},
		{"RZD_DATABASE_URL", "postgres://rzd_app:PLACEHOLDER@postgres:5432/postgres"},
		{"RZD_COMPUTE_URL", "http://example.com:8091"},
		{"RZD_COMPUTE_URL", "http://simulator:8091"},
		{"RZD_COMPUTE_URL", "http://compute:8091/foreign"},
		{"RZD_COMPUTE_URL", "http://USER:PLACEHOLDER@compute:8091"},
		{"RZD_SIMULATOR_URL", "http://example.com:8094"},
		{"RZD_SIMULATOR_URL", "http://compute:8094"},
		{"RZD_HTTP_ADDR", "192.0.2.1:8090"},
		{"RZD_HTTP_ADDR", "0.0.0.0:0"},
		{"RZD_HTTP_ADDR", "0.0.0.0:65536"},
		{"RZD_UI_ORIGIN", "http://web:8080"},
	} {
		t.Run(tc.key+"="+tc.value, func(t *testing.T) {
			t.Setenv(tc.key, tc.value)
			if _, err := Load(); err == nil {
				t.Fatal("unrelated deployment endpoint accepted")
			}
		})
	}
	t.Setenv("RZD_DEPLOYMENT_MODE", "")
	if _, err := Load(); err == nil {
		t.Fatal("container hosts enabled in the default local mode")
	}
}

func TestHTTPPortIsValidatedBeforeStartup(t *testing.T) {
	t.Setenv("RZD_DEPLOYMENT_MODE", "")
	t.Setenv("RZD_DATABASE_URL", "postgres://USER:PLACEHOLDER@127.0.0.1/rzd_arctica_demo")
	t.Setenv("RZD_COMPUTE_URL", "http://127.0.0.1:8091")
	t.Setenv("RZD_SIMULATOR_URL", "http://127.0.0.1:8094")
	t.Setenv("RZD_UI_ORIGIN", "")
	for _, port := range []string{"", "http", "0", "65536", "-1"} {
		t.Run(port, func(t *testing.T) {
			t.Setenv("RZD_HTTP_ADDR", "127.0.0.1:"+port)
			if _, err := Load(); err == nil {
				t.Fatal("invalid listener port accepted")
			}
		})
	}
}
