package configdb

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

// legacySchema mirrors the pre-GORM raw-SQL schema (schema.go/users.go/
// sso.go/securitylog.go before this rewrite), to prove AutoMigrate can
// bring an existing installation's database forward without losing data.
var legacySchema = []string{
	`CREATE TABLE config_scalar (
		id                INTEGER PRIMARY KEY CHECK (id = 1),
		api_base_url      TEXT NOT NULL DEFAULT '',
		api_username      TEXT NOT NULL DEFAULT '',
		api_password      TEXT NOT NULL DEFAULT '',
		api_token         TEXT NOT NULL DEFAULT '',
		db_dsn            TEXT NOT NULL DEFAULT '',
		db_table          TEXT NOT NULL DEFAULT '',
		db_prune_missing  INTEGER NOT NULL DEFAULT 0,
		db_sync_overlays  INTEGER NOT NULL DEFAULT 0
	)`,
	`CREATE TABLE database_columns (
		field  TEXT PRIMARY KEY,
		column TEXT NOT NULL
	)`,
	`CREATE TABLE maps (
		id         INTEGER PRIMARY KEY AUTOINCREMENT,
		map_id     TEXT NOT NULL,
		name       TEXT NOT NULL DEFAULT '',
		sort_order INTEGER NOT NULL,
		interval   TEXT NOT NULL DEFAULT '',
		disabled   INTEGER NOT NULL DEFAULT 0
	)`,
	`CREATE TABLE map_versions (
		id         INTEGER PRIMARY KEY AUTOINCREMENT,
		map_id     INTEGER NOT NULL REFERENCES maps(id) ON DELETE CASCADE,
		version    TEXT NOT NULL,
		sort_order INTEGER NOT NULL
	)`,
	`CREATE TABLE map_static_columns (
		id     INTEGER PRIMARY KEY AUTOINCREMENT,
		map_id INTEGER NOT NULL REFERENCES maps(id) ON DELETE CASCADE,
		column TEXT NOT NULL,
		value  TEXT NOT NULL,
		UNIQUE(map_id, column)
	)`,
	`CREATE UNIQUE INDEX idx_maps_map_id ON maps(map_id)`,
	`CREATE TABLE users (
		id                        INTEGER PRIMARY KEY AUTOINCREMENT,
		username                  TEXT NOT NULL UNIQUE,
		password_hash             TEXT NOT NULL,
		is_superuser              INTEGER NOT NULL DEFAULT 0,
		perm_view_status          INTEGER NOT NULL DEFAULT 0,
		perm_trigger_sync         INTEGER NOT NULL DEFAULT 0,
		perm_view_config          INTEGER NOT NULL DEFAULT 0,
		perm_edit_config_api      INTEGER NOT NULL DEFAULT 0,
		perm_edit_config_database INTEGER NOT NULL DEFAULT 0,
		perm_edit_config_maps     INTEGER NOT NULL DEFAULT 0,
		perm_edit_config_sso      INTEGER NOT NULL DEFAULT 0,
		created_at                TEXT NOT NULL
	)`,
	`CREATE TABLE sessions (
		token_hash TEXT PRIMARY KEY,
		user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		created_at TEXT NOT NULL,
		expires_at TEXT NOT NULL
	)`,
	`CREATE TABLE sso_config (
		id                            INTEGER PRIMARY KEY CHECK (id = 1),
		enabled                       INTEGER NOT NULL DEFAULT 0,
		issuer_url                    TEXT NOT NULL DEFAULT '',
		client_id                     TEXT NOT NULL DEFAULT '',
		client_secret                 TEXT NOT NULL DEFAULT '',
		scopes                        TEXT NOT NULL DEFAULT '',
		button_label                  TEXT NOT NULL DEFAULT '',
		redirect_base_url             TEXT NOT NULL DEFAULT '',
		default_view_status           INTEGER NOT NULL DEFAULT 0,
		default_trigger_sync          INTEGER NOT NULL DEFAULT 0,
		default_view_config           INTEGER NOT NULL DEFAULT 0,
		default_edit_config_api       INTEGER NOT NULL DEFAULT 0,
		default_edit_config_database  INTEGER NOT NULL DEFAULT 0,
		default_edit_config_maps      INTEGER NOT NULL DEFAULT 0,
		default_edit_config_sso       INTEGER NOT NULL DEFAULT 0
	)`,
	`CREATE TABLE sso_identities (
		id         INTEGER PRIMARY KEY AUTOINCREMENT,
		issuer     TEXT NOT NULL,
		subject    TEXT NOT NULL,
		user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		created_at TEXT NOT NULL,
		UNIQUE(issuer, subject)
	)`,
	`CREATE TABLE security_log (
		id          INTEGER PRIMARY KEY AUTOINCREMENT,
		at          TEXT NOT NULL,
		event_type  TEXT NOT NULL,
		username    TEXT NOT NULL DEFAULT '',
		remote_addr TEXT NOT NULL DEFAULT '',
		detail      TEXT NOT NULL DEFAULT ''
	)`,
}

// seedLegacyDatabase creates a SQLite file at dbPath using legacySchema (raw
// database/sql, no GORM involved) and inserts one row of sample data per
// table that Store.migrate needs to evolve, returning legacy-map's
// autoincrement row id for the caller to check cascade deletes against.
func seedLegacyDatabase(ctx context.Context, t *testing.T, dbPath string) (createdAt string, mapRowID int64) {
	t.Helper()

	raw, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open raw: %v", err)
	}
	defer func() {
		if err := raw.Close(); err != nil {
			t.Fatalf("close raw: %v", err)
		}
	}()

	if _, err := raw.ExecContext(ctx, "PRAGMA foreign_keys = ON"); err != nil {
		t.Fatalf("pragma: %v", err)
	}

	for _, stmt := range legacySchema {
		if _, err := raw.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("legacy schema stmt failed: %v\n%s", err, stmt)
		}
	}

	createdAt = time.Now().UTC().Format(time.RFC3339Nano)

	_, err = raw.ExecContext(ctx, `INSERT INTO config_scalar (id, api_base_url, api_token, db_dsn, db_table, db_prune_missing)
		VALUES (1, 'https://legacy.example', 'tok123', 'user:pass@/db', 'geo_objects', 1)`)
	if err != nil {
		t.Fatalf("insert config_scalar: %v", err)
	}

	_, err = raw.ExecContext(ctx, `INSERT INTO users (username, password_hash, is_superuser, perm_view_status, perm_edit_config_sso, created_at)
		VALUES ('legacy-admin', 'somehash', 1, 1, 1, ?)`, createdAt)
	if err != nil {
		t.Fatalf("insert user: %v", err)
	}

	res, err := raw.ExecContext(ctx,
		`INSERT INTO maps (map_id, name, sort_order, interval, disabled) VALUES ('legacy-map', 'Legacy Map', 0, '10m', 0)`)
	if err != nil {
		t.Fatalf("insert map: %v", err)
	}

	mapRowID, err = res.LastInsertId()
	if err != nil {
		t.Fatalf("map row id: %v", err)
	}

	_, err = raw.ExecContext(ctx,
		`INSERT INTO map_versions (map_id, version, sort_order) VALUES (?, 'current', 0)`, mapRowID)
	if err != nil {
		t.Fatalf("insert map_version: %v", err)
	}

	_, err = raw.ExecContext(ctx,
		`INSERT INTO security_log (at, event_type, username, remote_addr, detail)
		 VALUES (?, 'login_success', 'legacy-admin', '10.0.0.1', 'ok')`, createdAt)
	if err != nil {
		t.Fatalf("insert security_log: %v", err)
	}

	return createdAt, mapRowID
}

func assertLegacyConfigIntact(ctx context.Context, t *testing.T, s *Store) {
	t.Helper()

	cfg, err := s.Load(ctx)
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	if cfg.API.BaseURL != "https://legacy.example" || cfg.API.Token != "tok123" || !cfg.Database.PruneMissing {
		t.Errorf("legacy config_scalar lost: %+v", cfg)
	}

	if len(cfg.Maps) != 1 || cfg.Maps[0].ID != "legacy-map" || cfg.Maps[0].Interval != "10m" || len(cfg.Maps[0].Versions) != 1 {
		t.Errorf("legacy map lost: %+v", cfg.Maps)
	}
}

func assertLegacyUserIntact(ctx context.Context, t *testing.T, s *Store) *User {
	t.Helper()

	u, err := s.GetUser(ctx, 1)
	if err != nil {
		t.Fatalf("get legacy user: %v", err)
	}

	if u.Username != "legacy-admin" || !u.IsSuperuser || !u.Permissions.ViewStatus || !u.Permissions.EditConfigSSO {
		t.Errorf("legacy user lost: %+v", u)
	}

	return u
}

func assertLegacySecurityLogIntact(ctx context.Context, t *testing.T, s *Store) {
	t.Helper()

	entries, err := s.ListSecurityLog(ctx, 10)
	if err != nil || len(entries) != 1 {
		t.Fatalf("legacy security log lost: %v %v", entries, err)
	}

	if entries[0].At.IsZero() {
		t.Errorf("legacy RFC3339Nano timestamp did not parse: %+v", entries[0])
	}

	if entries[0].Username != "legacy-admin" {
		t.Errorf("legacy security log detail lost: %+v", entries[0])
	}
}

func TestLegacyUpgrade(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "legacy.db")
	_, mapRowID := seedLegacyDatabase(ctx, t, dbPath)

	// Opening with the new GORM-backed Store must migrate the legacy schema
	// in place without losing any of the data seeded above.
	s, err := Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("open upgraded: %v", err)
	}
	defer func() {
		if err := s.Close(); err != nil {
			t.Fatalf("close: %v", err)
		}
	}()

	assertLegacyConfigIntact(ctx, t, s)
	u := assertLegacyUserIntact(ctx, t, s)
	assertLegacySecurityLogIntact(ctx, t, s)
	assertLegacyCascadeDeletes(ctx, t, s, u, mapRowID)

	// Re-open again to make sure the migration is stable (idempotent) once
	// the schema has already been upgraded.
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	s2, err := Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("second open after upgrade: %v", err)
	}
	defer func() {
		if err := s2.Close(); err != nil {
			t.Fatalf("close reopened store: %v", err)
		}
	}()

	if _, err := s2.Load(ctx); err != nil {
		t.Fatalf("load after second open: %v", err)
	}
}

// assertLegacyCascadeDeletes checks that deleting a map/user on a database
// upgraded from the legacy schema still cascades to their child rows —
// fkTableStatements' real FK, not GORM's own constraint management, is what
// has to keep this working (see schema.go).
func assertLegacyCascadeDeletes(ctx context.Context, t *testing.T, s *Store, u *User, mapRowID int64) {
	t.Helper()

	token, _, err := s.CreateSession(ctx, u.ID, time.Hour)
	if err != nil {
		t.Fatalf("create session on upgraded db: %v", err)
	}

	if _, err := s.SessionUser(ctx, token); err != nil {
		t.Fatalf("session user on upgraded db: %v", err)
	}

	if err := s.DeleteMap(ctx, "legacy-map"); err != nil {
		t.Fatalf("delete legacy map: %v", err)
	}

	var orphanVersions int64
	if err := s.db.WithContext(ctx).Model(&mapVersion{}).Where("map_id = ?", mapRowID).Count(&orphanVersions).Error; err != nil {
		t.Fatalf("count orphan versions: %v", err)
	}

	if orphanVersions != 0 {
		t.Errorf("map_versions did not cascade-delete: %d rows left", orphanVersions)
	}

	if err := s.DeleteUser(ctx, u.ID); err != nil {
		t.Fatalf("delete legacy user: %v", err)
	}

	if _, err := s.SessionUser(ctx, token); !errors.Is(err, ErrSessionInvalid) {
		t.Errorf("sessions did not cascade-delete: SessionUser returned %v", err)
	}
}
