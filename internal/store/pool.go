package store

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"

	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" database/sql driver for goose
	"github.com/pressly/goose/v3"
)

// NewPool creates a pgx connection pool from a PostgreSQL URL and verifies
// connectivity with a ping, failing fast on a bad URL or unreachable server.
// The caller owns the pool and must Close it.
func NewPool(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, fmt.Errorf("store: create pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("store: ping: %w", err)
	}
	return pool, nil
}

// Migrate applies all pending goose migrations from fsys against the database at
// databaseURL. It runs them over a short-lived database/sql handle (goose's
// required type, opened via the pgx stdlib driver), separate from the app's
// long-lived pgx pool.
func Migrate(ctx context.Context, databaseURL string, fsys fs.FS) error {
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		return fmt.Errorf("store: open migration db: %w", err)
	}
	defer db.Close()

	goose.SetBaseFS(fsys)
	defer goose.SetBaseFS(nil)

	if err := goose.SetDialect("postgres"); err != nil {
		return fmt.Errorf("store: set goose dialect: %w", err)
	}
	if err := goose.UpContext(ctx, db, "migrations"); err != nil {
		return fmt.Errorf("store: run migrations: %w", err)
	}
	return nil
}
