// Package store persists geo objects fetched from tileserve-go into a
// MariaDB database.
package store

import (
	"context"
	"database/sql"
	"fmt"
	"go-sync-objects/internal/config"
	"go-sync-objects/internal/tileserve"
	"strings"

	// Registers the "mysql" driver with database/sql; never referenced
	// directly, only through sql.Open.
	_ "github.com/go-sql-driver/mysql"
)

// Store wraps a MariaDB connection along with the target table and column
// mapping geo objects are written to.
type Store struct {
	db      *sql.DB
	table   string
	columns map[string]string
}

// Open connects to MariaDB using dbCfg.DSN (e.g.
// "user:pass@tcp(127.0.0.1:3306)/dbname?parseTime=true"; parseTime=true is
// required so DATETIME columns scan into time.Time) and configures the
// target table/columns geo objects are synced to. dbCfg must already have
// passed config.Load's validation, which fills in Table/Columns defaults.
func Open(ctx context.Context, dbCfg config.Database) (*Store, error) {
	db, err := sql.Open("mysql", dbCfg.DSN)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}

	return &Store{db: db, table: dbCfg.Table, columns: dbCfg.Columns}, nil
}

// Close closes the underlying database connection.
func (s *Store) Close() error {
	return s.db.Close()
}

// col returns the configured column name for field, or "" if the field is
// skipped.
func (s *Store) col(field string) string {
	return s.columns[field]
}

// varcharDefaultEmpty is the SQL type shared by several string columns below.
const varcharDefaultEmpty = "VARCHAR(255) NOT NULL DEFAULT ''"

// fieldColumn pairs a GeoObject (or bookkeeping) field with the SQL type
// EnsureSchema gives it and the accessor UpsertGeoObjects reads its value
// through, keeping schema, field order, and value extraction in one place.
type fieldColumn struct {
	field   string
	sqlType string
	value   func(tileserve.GeoObject) any
}

// geoObjectColumns lists every syncable field in a stable order, matching
// the placeholder order upsertSQL generates.
var geoObjectColumns = []fieldColumn{
	{config.FieldUUID, "CHAR(36)     NOT NULL PRIMARY KEY", func(o tileserve.GeoObject) any { return o.UUID }},
	{config.FieldMapUUID, "CHAR(36)     NOT NULL", func(o tileserve.GeoObject) any { return o.MapUUID }},
	{config.FieldVersion, "VARCHAR(64)  NOT NULL", func(o tileserve.GeoObject) any { return o.Version }},
	{config.FieldName, "VARCHAR(255) NOT NULL", func(o tileserve.GeoObject) any { return o.Name }},
	{config.FieldExternalID, varcharDefaultEmpty, func(o tileserve.GeoObject) any { return o.ExternalID }},
	{config.FieldLatitude, "DOUBLE       NOT NULL", func(o tileserve.GeoObject) any { return o.Latitude }},
	{config.FieldLongitude, "DOUBLE       NOT NULL", func(o tileserve.GeoObject) any { return o.Longitude }},
	{config.FieldStreet, varcharDefaultEmpty, func(o tileserve.GeoObject) any { return o.Street }},
	{config.FieldHousenumber, "VARCHAR(64)  NOT NULL DEFAULT ''", func(o tileserve.GeoObject) any { return o.Housenumber }},
	{config.FieldPostcode, "VARCHAR(32)  NOT NULL DEFAULT ''", func(o tileserve.GeoObject) any { return o.Postcode }},
	{config.FieldCreatedAt, "DATETIME     NULL", func(o tileserve.GeoObject) any { return o.CreatedAt }},
	{config.FieldUpdatedAt, "DATETIME     NULL", func(o tileserve.GeoObject) any { return o.UpdatedAt }},
	{config.FieldCreatedBy, varcharDefaultEmpty, func(o tileserve.GeoObject) any { return o.CreatedBy }},
	{config.FieldUpdatedBy, varcharDefaultEmpty, func(o tileserve.GeoObject) any { return o.UpdatedBy }},
}

const syncedAtSQLType = "DATETIME     NOT NULL DEFAULT CURRENT_TIMESTAMP"

// EnsureSchema creates the target table if it doesn't already exist, with
// one column per non-skipped field plus the synced_at bookkeeping column
// (unless that too is skipped). It's a no-op against a table that already
// exists, so mapping onto a pre-existing external table is safe even when
// its schema doesn't match what's generated here.
func (s *Store) EnsureSchema(ctx context.Context) error {
	var defs []string

	for _, fc := range geoObjectColumns {
		if col := s.col(fc.field); col != "" {
			defs = append(defs, col+" "+fc.sqlType)
		}
	}

	if syncedAt := s.col(config.FieldSyncedAt); syncedAt != "" {
		defs = append(defs, syncedAt+" "+syncedAtSQLType)
	}

	if mapUUID, version := s.col(config.FieldMapUUID), s.col(config.FieldVersion); mapUUID != "" && version != "" {
		defs = append(defs, fmt.Sprintf("INDEX idx_%s_map_version (%s, %s)", s.table, mapUUID, version))
	}

	stmt := fmt.Sprintf("CREATE TABLE IF NOT EXISTS %s (\n\t%s\n) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;",
		s.table, strings.Join(defs, ",\n\t"))

	if _, err := s.db.ExecContext(ctx, stmt); err != nil {
		return fmt.Errorf("create %s table: %w", s.table, err)
	}

	return nil
}

// upsertSQL builds the INSERT ... ON DUPLICATE KEY UPDATE statement for the
// currently configured table/columns, along with the accessors its
// placeholders expect values from, in order. Fields mapped to "" are left
// out of the statement entirely.
func (s *Store) upsertSQL() (stmt string, accessors []func(tileserve.GeoObject) any) {
	var cols []string

	for _, fc := range geoObjectColumns {
		if col := s.col(fc.field); col != "" {
			cols = append(cols, col)
			accessors = append(accessors, fc.value)
		}
	}

	placeholders := strings.TrimSuffix(strings.Repeat("?, ", len(cols)), ", ")

	uuidCol := s.col(config.FieldUUID)

	var sets []string

	for _, col := range cols {
		if col == uuidCol {
			continue
		}

		sets = append(sets, fmt.Sprintf("%s = VALUES(%s)", col, col))
	}

	if syncedAt := s.col(config.FieldSyncedAt); syncedAt != "" {
		sets = append(sets, syncedAt+" = CURRENT_TIMESTAMP")
	}

	stmt = fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s) ON DUPLICATE KEY UPDATE %s;",
		s.table, strings.Join(cols, ", "), placeholders, strings.Join(sets, ", "))

	return stmt, accessors
}

// UpsertGeoObjects writes each geo object to the configured table, inserting
// new rows and updating existing ones (matched by the FieldUUID column). It
// runs inside a single transaction per call.
func (s *Store) UpsertGeoObjects(ctx context.Context, objects []tileserve.GeoObject) error {
	if len(objects) == 0 {
		return nil
	}

	stmt, accessors := s.upsertSQL()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	prepared, err := tx.PrepareContext(ctx, stmt)
	if err != nil {
		return fmt.Errorf("prepare upsert statement: %w", err)
	}
	defer func() { _ = prepared.Close() }()

	args := make([]any, len(accessors))

	for _, o := range objects {
		for i, accessor := range accessors {
			args[i] = accessor(o)
		}

		if _, err := prepared.ExecContext(ctx, args...); err != nil {
			return fmt.Errorf("upsert geo object %s: %w", o.UUID, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}

	return nil
}
