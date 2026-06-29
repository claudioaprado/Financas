package store

import (
	"context"
	"os"
	"testing"

	"github.com/claudioaprado/financas/db"
)

// testDatabaseURL returns a usable connection string or skips the test. The
// integration tests are gated so the default `go test ./...` stays green with
// no database available.
func testDatabaseURL(t *testing.T) string {
	t.Helper()
	for _, k := range []string{"TEST_DATABASE_URL", "DATABASE_URL"} {
		if v := os.Getenv(k); v != "" {
			return v
		}
	}
	t.Skip("set TEST_DATABASE_URL or DATABASE_URL to run store integration tests")
	return ""
}

func TestMigrateThenPool(t *testing.T) {
	url := testDatabaseURL(t)
	ctx := context.Background()

	if err := Migrate(ctx, url, db.Migrations); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}
	// Re-running must be idempotent (no pending migrations).
	if err := Migrate(ctx, url, db.Migrations); err != nil {
		t.Fatalf("Migrate() second run error = %v", err)
	}

	pool, err := NewPool(ctx, url)
	if err != nil {
		t.Fatalf("NewPool() error = %v", err)
	}
	defer pool.Close()

	var n int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM goose_db_version").Scan(&n); err != nil {
		t.Fatalf("query goose_db_version: %v", err)
	}
	if n < 1 {
		t.Fatalf("goose_db_version rows = %d, want >= 1 (baseline + applied)", n)
	}
}

func TestNewPoolBadURL(t *testing.T) {
	_, err := NewPool(context.Background(), "postgres://nope:nope@127.0.0.1:1/none?sslmode=disable&connect_timeout=1")
	if err == nil {
		t.Fatal("expected error for unreachable database")
	}
}
