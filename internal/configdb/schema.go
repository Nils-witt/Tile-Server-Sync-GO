package configdb

import (
	"context"
	"database/sql"
	"fmt"
)

// schemaStatements creates the core config tables: config_scalar (the
// singleton api/database scalar fields), database_columns, maps, and their
// per-map children map_versions/map_static_columns. The users/sessions
// schema (userSchemaStatements), the sso_config/sso_identities schema
// (ssoSchemaStatements), and the security_log schema
// (securityLogSchemaStatements) live next to the store methods that use
// them, in users.go/sso.go/securitylog.go respectively — ensureSchema below
// runs all four.
var schemaStatements = []string{
	`CREATE TABLE IF NOT EXISTS config_scalar (
		id               INTEGER PRIMARY KEY CHECK (id = 1),
		api_base_url     TEXT NOT NULL DEFAULT '',
		api_username     TEXT NOT NULL DEFAULT '',
		api_password     TEXT NOT NULL DEFAULT '',
		api_token        TEXT NOT NULL DEFAULT '',
		db_dsn           TEXT NOT NULL DEFAULT '',
		db_table         TEXT NOT NULL DEFAULT '',
		db_prune_missing INTEGER NOT NULL DEFAULT 0
	)`,
	`CREATE TABLE IF NOT EXISTS database_columns (
		field  TEXT PRIMARY KEY,
		column TEXT NOT NULL
	)`,
	`CREATE TABLE IF NOT EXISTS maps (
		id         INTEGER PRIMARY KEY AUTOINCREMENT,
		map_id     TEXT NOT NULL,
		sort_order INTEGER NOT NULL,
		interval   TEXT NOT NULL DEFAULT ''
	)`,
	`CREATE TABLE IF NOT EXISTS map_versions (
		id         INTEGER PRIMARY KEY AUTOINCREMENT,
		map_id     INTEGER NOT NULL REFERENCES maps(id) ON DELETE CASCADE,
		version    TEXT NOT NULL,
		sort_order INTEGER NOT NULL
	)`,
	`CREATE TABLE IF NOT EXISTS map_static_columns (
		id     INTEGER PRIMARY KEY AUTOINCREMENT,
		map_id INTEGER NOT NULL REFERENCES maps(id) ON DELETE CASCADE,
		column TEXT NOT NULL,
		value  TEXT NOT NULL,
		UNIQUE(map_id, column)
	)`,
	`CREATE INDEX IF NOT EXISTS idx_map_versions_map_id ON map_versions(map_id)`,
	`CREATE INDEX IF NOT EXISTS idx_map_static_columns_map_id ON map_static_columns(map_id)`,
}

// ensureSchema runs every table's CREATE TABLE IF NOT EXISTS statements,
// then every migration that backfills a column IF NOT EXISTS can't express
// (SQLite has no ALTER TABLE ADD COLUMN IF NOT EXISTS) on a database created
// by an older version of this schema.
func (s *Store) ensureSchema(ctx context.Context) error {
	for _, stmt := range schemaStatements {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("create config schema: %w", err)
		}
	}

	for _, stmt := range userSchemaStatements {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("create user schema: %w", err)
		}
	}

	for _, stmt := range ssoSchemaStatements {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("create sso schema: %w", err)
		}
	}

	for _, stmt := range securityLogSchemaStatements {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("create security log schema: %w", err)
		}
	}

	if err := s.migrateMapsInterval(ctx); err != nil {
		return err
	}

	if err := s.ensureMapsUniqueIndex(ctx); err != nil {
		return err
	}

	return s.migrateUsersEditConfigSSO(ctx)
}

// migrateMapsInterval adds the maps.interval column to a database created by
// an older version of this schema (back when the sync interval was a single
// global config_scalar column instead of per-map), where the maps CREATE
// TABLE statement above — guarded by IF NOT EXISTS — never ran again to add
// it. A freshly created database already has the column from that
// statement, so this checks first via PRAGMA table_info to stay idempotent.
func (s *Store) migrateMapsInterval(ctx context.Context) error {
	hasInterval, err := s.tableHasColumn(ctx, `PRAGMA table_info(maps)`, "interval")
	if err != nil {
		return err
	}

	if hasInterval {
		return nil
	}

	if _, err := s.db.ExecContext(ctx, `ALTER TABLE maps ADD COLUMN interval TEXT NOT NULL DEFAULT ''`); err != nil {
		return fmt.Errorf("add maps.interval column: %w", err)
	}

	return nil
}

// migrateUsersEditConfigSSO adds the users.perm_edit_config_sso column to a
// database created before the SSO feature existed, the same way
// migrateMapsInterval backfills maps.interval on an older schema.
func (s *Store) migrateUsersEditConfigSSO(ctx context.Context) error {
	hasColumn, err := s.tableHasColumn(ctx, `PRAGMA table_info(users)`, "perm_edit_config_sso")
	if err != nil {
		return err
	}

	if hasColumn {
		return nil
	}

	if _, err := s.db.ExecContext(ctx,
		`ALTER TABLE users ADD COLUMN perm_edit_config_sso INTEGER NOT NULL DEFAULT 0`); err != nil {
		return fmt.Errorf("add users.perm_edit_config_sso column: %w", err)
	}

	return nil
}

// tableHasColumn reports whether the table targeted by the literal
// "PRAGMA table_info(...)" statement pragmaQuery already has a column named
// col.
func (s *Store) tableHasColumn(ctx context.Context, pragmaQuery, col string) (bool, error) {
	rows, err := s.db.QueryContext(ctx, pragmaQuery)
	if err != nil {
		return false, fmt.Errorf("inspect table (%s): %w", pragmaQuery, err)
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var (
			cid, notNull, pk int
			name, colType    string
			defaultValue     sql.NullString
		)

		if err := rows.Scan(&cid, &name, &colType, &notNull, &defaultValue, &pk); err != nil {
			return false, fmt.Errorf("inspect table (%s): %w", pragmaQuery, err)
		}

		if name == col {
			return true, nil
		}
	}

	if err := rows.Err(); err != nil {
		return false, fmt.Errorf("inspect table (%s): %w", pragmaQuery, err)
	}

	return false, nil
}
