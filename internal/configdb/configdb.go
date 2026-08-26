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
	"errors"
	"fmt"
	"go-sync-objects/internal/config"

	// Registers the "sqlite" driver with database/sql; never referenced
	// directly, only through sql.Open.
	_ "modernc.org/sqlite"
)

// Store wraps a SQLite connection holding the config database.
type Store struct {
	db *sql.DB
}

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

// Open opens (creating if needed) the SQLite database at path and ensures
// its schema exists. The connection pool is pinned to a single connection
// so the "PRAGMA foreign_keys = ON" set here (needed for the "ON DELETE
// CASCADE" clauses above to actually fire on Save) reliably applies to
// every statement this Store ever runs — SQLite pragmas are per-connection,
// and database/sql's pool would otherwise silently hand out a fresh,
// pragma-less connection under load. Config reads/writes are infrequent
// (driven by the web UI, not the sync hot path), so serializing them
// through one connection has no meaningful cost.
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

func (s *Store) ensureSchema(ctx context.Context) error {
	for _, stmt := range schemaStatements {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("create config schema: %w", err)
		}
	}

	return s.migrateMapsInterval(ctx)
}

// migrateMapsInterval adds the maps.interval column to a database created by
// an older version of this schema (back when the sync interval was a single
// global config_scalar column instead of per-map), where the maps CREATE
// TABLE statement above — guarded by IF NOT EXISTS — never ran again to add
// it. A freshly created database already has the column from that
// statement, so this checks first via PRAGMA table_info to stay idempotent.
func (s *Store) migrateMapsInterval(ctx context.Context) error {
	hasInterval, err := s.mapsTableHasColumn(ctx, "interval")
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

// mapsTableHasColumn reports whether the maps table already has a column
// named col, via PRAGMA table_info.
func (s *Store) mapsTableHasColumn(ctx context.Context, col string) (bool, error) {
	rows, err := s.db.QueryContext(ctx, `PRAGMA table_info(maps)`)
	if err != nil {
		return false, fmt.Errorf("inspect maps table: %w", err)
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var (
			cid, notNull, pk int
			name, colType    string
			defaultValue     sql.NullString
		)

		if err := rows.Scan(&cid, &name, &colType, &notNull, &defaultValue, &pk); err != nil {
			return false, fmt.Errorf("inspect maps table: %w", err)
		}

		if name == col {
			return true, nil
		}
	}

	if err := rows.Err(); err != nil {
		return false, fmt.Errorf("inspect maps table: %w", err)
	}

	return false, nil
}

// Load assembles a *config.Config from the stored rows. WebServer is left
// zero-valued — callers overlay the fixed bootstrap value. An empty
// database (no config_scalar row yet, e.g. a brand new install) is not an
// error: it returns a zero-valued Config, exactly as an empty YAML document
// would have under the old file-backed Load. Callers decide whether that
// state is usable by calling cfg.Validate().
func (s *Store) Load(ctx context.Context) (*config.Config, error) {
	cfg := &config.Config{}

	var pruneMissing int64

	row := s.db.QueryRowContext(ctx,
		`SELECT api_base_url, api_username, api_password, api_token, db_dsn, db_table, db_prune_missing
		 FROM config_scalar WHERE id = 1`)

	switch err := row.Scan(
		&cfg.API.BaseURL, &cfg.API.Username, &cfg.API.Password, &cfg.API.Token,
		&cfg.Database.DSN, &cfg.Database.Table, &pruneMissing,
	); {
	case errors.Is(err, sql.ErrNoRows):
		// No row yet: leave cfg's scalars zero-valued.
	case err != nil:
		return nil, fmt.Errorf("load config: %w", err)
	default:
		cfg.Database.PruneMissing = pruneMissing != 0
	}

	columns, err := s.loadDatabaseColumns(ctx)
	if err != nil {
		return nil, err
	}

	cfg.Database.Columns = columns

	maps, err := s.loadMaps(ctx)
	if err != nil {
		return nil, err
	}

	cfg.Maps = maps

	return cfg, nil
}

func (s *Store) loadDatabaseColumns(ctx context.Context) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT field, column FROM database_columns`)
	if err != nil {
		return nil, fmt.Errorf("load database columns: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var columns map[string]string

	for rows.Next() {
		var field, column string
		if err := rows.Scan(&field, &column); err != nil {
			return nil, fmt.Errorf("scan database column: %w", err)
		}

		if columns == nil {
			columns = make(map[string]string)
		}

		columns[field] = column
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("load database columns: %w", err)
	}

	return columns, nil
}

// mapRow is one maps table row, before its versions/staticColumns are
// loaded.
type mapRow struct {
	rowID  int64
	target config.MapTarget
}

func (s *Store) loadMaps(ctx context.Context) ([]config.MapTarget, error) {
	mapRows, err := s.queryMapRows(ctx)
	if err != nil {
		return nil, err
	}

	if mapRows == nil {
		return nil, nil
	}

	targets := make([]config.MapTarget, len(mapRows))

	for i, mr := range mapRows {
		versions, err := s.loadMapVersions(ctx, mr.rowID)
		if err != nil {
			return nil, err
		}

		staticColumns, err := s.loadMapStaticColumns(ctx, mr.rowID)
		if err != nil {
			return nil, err
		}

		mr.target.Versions = versions
		mr.target.StaticColumns = staticColumns
		targets[i] = mr.target
	}

	return targets, nil
}

// queryMapRows reads the maps table's id/map_id columns, in configured
// order, without yet loading each map's versions/staticColumns.
func (s *Store) queryMapRows(ctx context.Context) ([]mapRow, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, map_id, interval FROM maps ORDER BY sort_order ASC, id ASC`)
	if err != nil {
		return nil, fmt.Errorf("load maps: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var mapRows []mapRow

	for rows.Next() {
		var mr mapRow
		if err := rows.Scan(&mr.rowID, &mr.target.ID, &mr.target.Interval); err != nil {
			return nil, fmt.Errorf("scan map: %w", err)
		}

		mapRows = append(mapRows, mr)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("load maps: %w", err)
	}

	return mapRows, nil
}

func (s *Store) loadMapVersions(ctx context.Context, mapRowID int64) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT version FROM map_versions WHERE map_id = ? ORDER BY sort_order ASC, id ASC`, mapRowID)
	if err != nil {
		return nil, fmt.Errorf("load map versions: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var versions []string

	for rows.Next() {
		var version string
		if err := rows.Scan(&version); err != nil {
			return nil, fmt.Errorf("scan map version: %w", err)
		}

		versions = append(versions, version)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("load map versions: %w", err)
	}

	return versions, nil
}

func (s *Store) loadMapStaticColumns(ctx context.Context, mapRowID int64) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT column, value FROM map_static_columns WHERE map_id = ?`, mapRowID)
	if err != nil {
		return nil, fmt.Errorf("load map static columns: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var columns map[string]string

	for rows.Next() {
		var column, value string
		if err := rows.Scan(&column, &value); err != nil {
			return nil, fmt.Errorf("scan map static column: %w", err)
		}

		if columns == nil {
			columns = make(map[string]string)
		}

		columns[column] = value
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("load map static columns: %w", err)
	}

	return columns, nil
}

// Save persists every field of cfg except WebServer, replacing the
// database_columns and maps (with their versions/staticColumns) tables
// wholesale inside one transaction — a delete-then-reinsert rather than a
// diff, matching the web UI's whole-form save semantics. Row ids churn on
// every save; nothing outside this package references them. Save does not
// call cfg.Validate() itself — callers validate before calling Save.
func (s *Store) Save(ctx context.Context, cfg *config.Config) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	pruneMissing := 0
	if cfg.Database.PruneMissing {
		pruneMissing = 1
	}

	_, err = tx.ExecContext(ctx, `
		INSERT INTO config_scalar
			(id, api_base_url, api_username, api_password, api_token, db_dsn, db_table, db_prune_missing)
		VALUES (1, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			api_base_url = excluded.api_base_url,
			api_username = excluded.api_username,
			api_password = excluded.api_password,
			api_token = excluded.api_token,
			db_dsn = excluded.db_dsn,
			db_table = excluded.db_table,
			db_prune_missing = excluded.db_prune_missing`,
		cfg.API.BaseURL, cfg.API.Username, cfg.API.Password, cfg.API.Token,
		cfg.Database.DSN, cfg.Database.Table, pruneMissing)
	if err != nil {
		return fmt.Errorf("save config: %w", err)
	}

	if err := saveDatabaseColumns(ctx, tx, cfg.Database.Columns); err != nil {
		return err
	}

	if err := saveMaps(ctx, tx, cfg.Maps); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}

	return nil
}

func saveDatabaseColumns(ctx context.Context, tx *sql.Tx, columns map[string]string) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM database_columns`); err != nil {
		return fmt.Errorf("clear database columns: %w", err)
	}

	for field, column := range columns {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO database_columns (field, column) VALUES (?, ?)`, field, column); err != nil {
			return fmt.Errorf("save database column %q: %w", field, err)
		}
	}

	return nil
}

func saveMaps(ctx context.Context, tx *sql.Tx, maps []config.MapTarget) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM maps`); err != nil {
		return fmt.Errorf("clear maps: %w", err)
	}

	for i, m := range maps {
		res, err := tx.ExecContext(ctx,
			`INSERT INTO maps (map_id, sort_order, interval) VALUES (?, ?, ?)`, m.ID, i, m.Interval)
		if err != nil {
			return fmt.Errorf("save map %q: %w", m.ID, err)
		}

		mapRowID, err := res.LastInsertId()
		if err != nil {
			return fmt.Errorf("get row id for map %q: %w", m.ID, err)
		}

		for j, version := range m.Versions {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO map_versions (map_id, version, sort_order) VALUES (?, ?, ?)`,
				mapRowID, version, j); err != nil {
				return fmt.Errorf("save version %q for map %q: %w", version, m.ID, err)
			}
		}

		for column, value := range m.StaticColumns {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO map_static_columns (map_id, column, value) VALUES (?, ?, ?)`,
				mapRowID, column, value); err != nil {
				return fmt.Errorf("save static column %q for map %q: %w", column, m.ID, err)
			}
		}
	}

	return nil
}
