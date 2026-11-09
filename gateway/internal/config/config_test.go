package config

import "testing"

// load sets the settings every test needs, then the ones under test, and loads the config.
func load(t *testing.T, env map[string]string) Config {
	t.Helper()
	t.Setenv("JWT_SECRET", "a-long-random-secret-for-the-tests-0123456789")
	for k, v := range env {
		t.Setenv(k, v)
	}
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

// A real server must not trust a developer's localhost: with CORS_ORIGINS unset or empty (which
// is how docker compose passes an unset value) production allows no browser origin at all.
func TestProductionHasNoCORSOriginsByDefault(t *testing.T) {
	for name, env := range map[string]map[string]string{
		"unset": {"APP_ENV": "production", "CORS_ORIGINS": ""},
		"empty": {"APP_ENV": "production"},
	} {
		if got := load(t, env).CORSOrigins; got != "" {
			t.Errorf("%s: production CORS origins = %q, want none", name, got)
		}
	}
}

// On a developer's machine the local web app works without any setting.
func TestDevelopmentAllowsTheLocalWebApp(t *testing.T) {
	if got := load(t, map[string]string{"CORS_ORIGINS": ""}).CORSOrigins; got != "http://localhost:3000" {
		t.Errorf("development CORS origins = %q, want http://localhost:3000", got)
	}
}

// What is set explicitly always wins, in production too.
func TestExplicitCORSOriginsWin(t *testing.T) {
	cfg := load(t, map[string]string{"APP_ENV": "production", "CORS_ORIGINS": "https://app.example.com"})
	if cfg.CORSOrigins != "https://app.example.com" {
		t.Errorf("CORS origins = %q", cfg.CORSOrigins)
	}
}
