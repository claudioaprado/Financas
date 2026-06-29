// Package store persists and reads data via pgx and sqlc-generated queries.
// It never decides business logic and never imports service (AD-1); amounts
// are stored in their native currency and never pre-converted (AD-5).
//
// It owns database wiring — the pgx connection pool (NewPool) and the
// on-startup goose migration runner (Migrate). sqlc generates type-safe query
// code into this package from db/query against the goose schema in db/migrations.
package store
