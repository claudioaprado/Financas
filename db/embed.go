// Package db holds the database schema migrations and embeds them so the single
// binary can run pending migrations on startup without shipping loose files.
package db

import "embed"

// Migrations is the embedded goose migration set (db/migrations/*.sql), applied
// on startup by internal/store.Migrate.
//
//go:embed migrations/*.sql
var Migrations embed.FS
