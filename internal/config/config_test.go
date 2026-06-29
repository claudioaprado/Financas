package config

import (
	"strings"
	"testing"
)

// setRequired sets all required vars to valid values for the current test.
func setRequired(t *testing.T) {
	t.Helper()
	t.Setenv("DATABASE_URL", "postgres://x")
	t.Setenv("SESSION_SECRET", "s")
	t.Setenv("OWNER_USERNAME", "owner")
	t.Setenv("OWNER_PASSWORD_HASH", "$argon2id$v=19$m=65536,t=1,p=2$abc$def")
}

func TestLoadSuccess(t *testing.T) {
	setRequired(t)
	t.Setenv("PORT", "9090")
	t.Setenv("SECURE_COOKIES", "true")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.DatabaseURL != "postgres://x" || cfg.SessionSecret != "s" || cfg.Port != "9090" {
		t.Fatalf("unexpected config: %+v", cfg)
	}
	if cfg.OwnerUsername != "owner" || cfg.OwnerPasswordHash == "" {
		t.Fatalf("owner not loaded: %+v", cfg)
	}
	if !cfg.SecureCookies {
		t.Fatal("SecureCookies should be true")
	}
}

func TestLoadDefaults(t *testing.T) {
	setRequired(t)
	t.Setenv("PORT", "")
	t.Setenv("SECURE_COOKIES", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Port != "8080" {
		t.Fatalf("Port = %q, want default 8080", cfg.Port)
	}
	if cfg.SecureCookies {
		t.Fatal("SecureCookies should default to false")
	}
}

func TestLoadMissingRequired(t *testing.T) {
	// Set none — every required var should be reported.
	t.Setenv("DATABASE_URL", "")
	t.Setenv("SESSION_SECRET", "")
	t.Setenv("OWNER_USERNAME", "")
	t.Setenv("OWNER_PASSWORD_HASH", "")

	_, err := Load()
	if err == nil {
		t.Fatal("expected error when required vars are missing")
	}
	for _, want := range []string{"DATABASE_URL", "SESSION_SECRET", "OWNER_USERNAME", "OWNER_PASSWORD_HASH"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should name %s, got: %v", want, err)
		}
	}
}

func TestParseBool(t *testing.T) {
	for _, s := range []string{"1", "true", "TRUE", "yes", "On"} {
		if !parseBool(s) {
			t.Errorf("parseBool(%q) = false, want true", s)
		}
	}
	for _, s := range []string{"", "0", "false", "no", "nope"} {
		if parseBool(s) {
			t.Errorf("parseBool(%q) = true, want false", s)
		}
	}
}
