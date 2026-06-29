// Package config loads application configuration from the environment. It is
// infrastructure read once at startup (cmd/server wires the app from it); it
// carries no financial logic and is imported by main, not by the financial
// layers (AD-1). Secrets are never logged.
package config

import (
	"fmt"
	"os"
	"strings"
)

// Config holds the environment-driven settings the server needs to boot.
type Config struct {
	DatabaseURL       string // required: PostgreSQL connection string (pgx)
	SessionSecret     string // required: key for signing sessions
	Port              string // listen port; defaults to 8080
	OwnerUsername     string // required: the single owner's username
	OwnerPasswordHash string // required: the owner's argon2id PHC hash (not plaintext)
	SecureCookies     bool   // SECURE_COOKIES: true behind HTTPS (Azure), false for local http
}

// Load reads configuration from the environment, applies defaults, and
// validates that required variables are present. The returned error names any
// missing variable; it never includes secret values.
func Load() (Config, error) {
	cfg := Config{
		DatabaseURL:       os.Getenv("DATABASE_URL"),
		SessionSecret:     os.Getenv("SESSION_SECRET"),
		Port:              envOr("PORT", "8080"),
		OwnerUsername:     os.Getenv("OWNER_USERNAME"),
		OwnerPasswordHash: os.Getenv("OWNER_PASSWORD_HASH"),
		SecureCookies:     parseBool(os.Getenv("SECURE_COOKIES")),
	}

	var missing []string
	for _, v := range []struct {
		name, val string
	}{
		{"DATABASE_URL", cfg.DatabaseURL},
		{"SESSION_SECRET", cfg.SessionSecret},
		{"OWNER_USERNAME", cfg.OwnerUsername},
		{"OWNER_PASSWORD_HASH", cfg.OwnerPasswordHash},
	} {
		if v.val == "" {
			missing = append(missing, v.name)
		}
	}
	if len(missing) > 0 {
		return Config{}, fmt.Errorf("config: missing required environment variable(s): %v", missing)
	}
	return cfg, nil
}

// envOr returns the value of the environment variable named by key, or def when
// it is unset or empty.
func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// parseBool reports whether s is a truthy flag value (true/1/yes, any case).
func parseBool(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}
