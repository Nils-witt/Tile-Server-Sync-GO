// Package configdb persists the DB-backed subset of config.Config — every
// field except WebServer — in a SQLite database, as a normalized relational
// schema mirroring Config's nested shape (maps, per-map versions, per-map
// static columns, database column overrides) rather than a serialized blob.
//
// This is unrelated to internal/store, which persists synced geo objects
// into a MariaDB database; the two packages share no code and serve
// entirely different databases.
package configdb

import (
	"context"
	"database/sql"
	"fmt"

	// Registers the "sqlite" driver with database/sql; never referenced
	// directly, only through sql.Open.
	_ "modernc.org/sqlite"
)

// Store wraps a SQLite connection holding the config database. Its schema
// (core config tables plus users/sso/security_log — see schema.go) and its
// Load/Save methods (load.go/save.go) for the config.Config-shaped subset
// live in their own files; per-row CRUD for maps/users/sso lives in
// maps.go/users.go/sso.go instead of going through the whole-graph
// Load/Save.
type Store struct {
	db *sql.DB
}

// Open opens (creating if needed) the SQLite database at path and ensures
// its schema exists. The connection pool is pinned to a single connection
// so the "PRAGMA foreign_keys = ON" set here (needed for the "ON DELETE
// CASCADE" clauses in schema.go to actually fire on Save) reliably applies
// to every statement this Store ever runs — SQLite pragmas are
// per-connection, and database/sql's pool would otherwise silently hand out
// a fresh, pragma-less connection under load. Config reads/writes are
// infrequent (driven by the web UI, not the sync hot path), so serializing
// them through one connection has no meaningful cost.
func Open(ctx context.Context, path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open config database: %w", err)
	}

	db.SetMaxOpenConns(1)

	if _, err := db.ExecContext(ctx, "PRAGMA foreign_keys = ON"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("enable foreign keys: %w", err)
	}

	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping config database: %w", err)
	}

	s := &Store{db: db}

	if err := s.ensureSchema(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}

	return s, nil
}

// Close closes the underlying database connection.
func (s *Store) Close() error {
	return s.db.Close()
}
