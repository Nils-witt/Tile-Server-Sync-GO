// Package configdb persists the DB-backed subset of config.Config — every
// field except WebServer — in a SQLite database, as a normalized relational
// schema mirroring Config's nested shape (maps, per-map versions, per-map
// static columns, database column overrides) rather than a serialized blob.
// It's backed by GORM (see schema.go for the table models and migration,
// and glebarez/sqlite — a pure-Go, cgo-free SQLite driver — so
// cross-compiled and Windows-service builds keep working).
//
// This is unrelated to internal/store, which persists synced geo objects
// into a MariaDB database; the two packages share no code and serve
// entirely different databases.
package configdb

import (
	"context"
	"fmt"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Store wraps a SQLite connection holding the config database. Its schema
// (core config tables plus users/sso/security_log — see schema.go) and its
// Load/Save methods (load.go/save.go) for the config.Config-shaped subset
// live in their own files; per-row CRUD for maps/users/sso lives in
// maps.go/users.go/sso.go instead of going through the whole-graph
// Load/Save.
type Store struct {
	db *gorm.DB
}

// Open opens (creating if needed) the SQLite database at path and migrates
// its schema. The connection pool is pinned to a single connection so the
// "PRAGMA foreign_keys = ON" set here (needed for the "ON DELETE CASCADE"
// clauses in schema.go to actually fire) reliably applies to every
// statement this Store ever runs — SQLite pragmas are per-connection, and
// database/sql's pool would otherwise silently hand out a fresh,
// pragma-less connection under load. Config reads/writes are infrequent
// (driven by the web UI, not the sync hot path), so serializing them
// through one connection has no meaningful cost.
func Open(ctx context.Context, path string) (*Store, error) {
	gdb, err := gorm.Open(sqlite.Open(path), &gorm.Config{
		Logger:         logger.Default.LogMode(logger.Silent),
		TranslateError: true,
	})
	if err != nil {
		return nil, fmt.Errorf("open config database: %w", err)
	}

	sqlDB, err := gdb.DB()
	if err != nil {
		return nil, fmt.Errorf("open config database: %w", err)
	}

	sqlDB.SetMaxOpenConns(1)

	if err := sqlDB.PingContext(ctx); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("ping config database: %w", err)
	}

	s := &Store{db: gdb}

	// Foreign keys are only enforced after migrating: both AutoMigrate and
	// rebuildLegacyUsersTable recreate tables (copy, drop, rename), and
	// dropping users with enforcement on would cascade-delete every session
	// and SSO link. PRAGMA foreign_keys is a no-op inside a transaction, so
	// this can't be done per migration step instead.
	if err := s.migrate(ctx); err != nil {
		_ = sqlDB.Close()
		return nil, err
	}

	if err := gdb.Exec("PRAGMA foreign_keys = ON").Error; err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("enable foreign keys: %w", err)
	}

	return s, nil
}

// Close closes the underlying database connection.
func (s *Store) Close() error {
	sqlDB, err := s.db.DB()
	if err != nil {
		return fmt.Errorf("close config database: %w", err)
	}

	return sqlDB.Close()
}
