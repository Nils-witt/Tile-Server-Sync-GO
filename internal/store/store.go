// Package store persists geo objects fetched from tileserve-go into a
// MariaDB database.
package store

import (
	"context"
	"database/sql"
	"fmt"
	"go-sync-objects/internal/config"
	"go-sync-objects/internal/tileserve"
	"log"
	"strings"
	"time"

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
	// staticColumns is the sorted, de-duplicated set of extra column names
	// referenced by any map's staticColumns (config.Config.StaticColumnNames).
	// It fixes the column order used by both EnsureSchema and the upsert
	// statement built by upsertSQL.
	staticColumns []string
	// pruneMissing mirrors config.Database.PruneMissing: when true,
	// UpsertGeoObjects deletes rows for the map_uuid/version scopes it
	// upserts that weren't present in the objects it was given.
	pruneMissing bool
}

// Open connects to MariaDB using dbCfg.DSN (e.g.
// "user:pass@tcp(127.0.0.1:3306)/dbname?parseTime=true"; parseTime=true is
// required so DATETIME columns scan into time.Time) and configures the
// target table/columns geo objects are synced to. dbCfg must already have
// passed config.Load's validation, which fills in Table/Columns defaults.
// staticColumns is the full set of extra static-value column names across
// all configured maps (see config.Config.StaticColumnNames).
func Open(ctx context.Context, dbCfg config.Database, staticColumns []string) (*Store, error) {
	db, err := sql.Open("mysql", dbCfg.DSN)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}

	// Sync runs are effectively single-writer (UpsertGeoObjects's caller
	// serializes scheduled and manual "sync now" runs via runtime.syncMu), so
	// a handful of connections is plenty; the lifetime/idle-time limits exist
	// so a connection sitting idle across a long `interval` gets recycled
	// instead of going stale and erroring on the next sync (e.g. a firewall
	// or MariaDB's wait_timeout closing it from the server side first).
	db.SetMaxOpenConns(5)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(5 * time.Minute)
	db.SetConnMaxIdleTime(1 * time.Minute)

	return &Store{
		db:            db,
		table:         dbCfg.Table,
		columns:       dbCfg.Columns,
		staticColumns: staticColumns,
		pruneMissing:  dbCfg.PruneMissing,
	}, nil
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
	{config.FieldCity, varcharDefaultEmpty, func(o tileserve.GeoObject) any { return o.City }},
	{config.FieldCityDistrict, varcharDefaultEmpty, func(o tileserve.GeoObject) any { return o.CityDistrict }},
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

	for _, col := range s.staticColumns {
		defs = append(defs, col+" "+varcharDefaultEmpty)
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

// upsertColumns returns the ordered column list (syncable GeoObject fields
// plus static columns) and the accessors that read each field's value, in
// the same order upsertSQL emits placeholders.
func (s *Store) upsertColumns() (cols []string, accessors []func(tileserve.GeoObject) any) {
	for _, fc := range geoObjectColumns {
		if col := s.col(fc.field); col != "" {
			cols = append(cols, col)
			accessors = append(accessors, fc.value)
		}
	}

	cols = append(cols, s.staticColumns...)

	return cols, accessors
}

// upsertSQL builds an INSERT ... ON DUPLICATE KEY UPDATE statement for cols
// with rows value-tuples, so upsertObjects can write a whole batch of geo
// objects in a single round trip instead of one per row.
func (s *Store) upsertSQL(cols []string, rows int) string {
	rowPlaceholder := "(" + strings.TrimSuffix(strings.Repeat("?, ", len(cols)), ", ") + ")"
	values := strings.TrimSuffix(strings.Repeat(rowPlaceholder+", ", rows), ", ")

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

	return fmt.Sprintf("INSERT INTO %s (%s) VALUES %s ON DUPLICATE KEY UPDATE %s;",
		s.table, strings.Join(cols, ", "), values, strings.Join(sets, ", "))
}

// UpsertGeoObjects writes each geo object to the configured table, inserting
// new rows and updating existing ones (matched by the FieldUUID column). It
// runs inside a single transaction per call. staticValues supplies the
// values for this map's configured staticColumns (config.MapTarget); any
// column in s.staticColumns absent from staticValues is written as "".
// mapUUID and version identify the map/version objects was fetched for
// (main.go's m.ID and the configured version string) and, when pruning is
// enabled, scope the delete even if objects is empty — an object list alone
// can't tell us that, since there's nothing in it to read a scope from.
func (s *Store) UpsertGeoObjects(
	ctx context.Context, objects []tileserve.GeoObject, staticValues map[string]string, mapUUID, version string,
) error {
	if len(objects) == 0 && !s.pruneMissing {
		return nil
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if len(objects) > 0 {
		if err := s.upsertObjects(ctx, tx, objects, staticValues); err != nil {
			return err
		}
	}

	if s.pruneMissing {
		keepUUIDs := make([]string, len(objects))
		for i, o := range objects {
			keepUUIDs[i] = o.UUID
		}

		if err := s.pruneMissingRows(ctx, tx, mapUUID, version, keepUUIDs); err != nil {
			return err
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}

	return nil
}

// upsertBatchRows caps how many geo objects go into a single INSERT
// statement: large enough to cut round trips dramatically for a big sync,
// small enough to stay well under MariaDB's default max_allowed_packet and
// the driver's placeholder limits.
const upsertBatchRows = 500

// upsertObjects writes objects to the configured table in batches of up to
// upsertBatchRows, each as one multi-row INSERT ... ON DUPLICATE KEY UPDATE
// within tx, rather than one round trip per row.
func (s *Store) upsertObjects(
	ctx context.Context, tx *sql.Tx, objects []tileserve.GeoObject, staticValues map[string]string,
) error {
	cols, accessors := s.upsertColumns()

	staticArgs := make([]any, len(s.staticColumns))
	for i, col := range s.staticColumns {
		staticArgs[i] = staticValues[col]
	}

	rowWidth := len(accessors) + len(staticArgs)

	for start := 0; start < len(objects); start += upsertBatchRows {
		end := min(start+upsertBatchRows, len(objects))
		batch := objects[start:end]

		args := make([]any, 0, rowWidth*len(batch))

		for _, o := range batch {
			for _, accessor := range accessors {
				args = append(args, accessor(o))
			}

			args = append(args, staticArgs...)
		}

		if _, err := tx.ExecContext(ctx, s.upsertSQL(cols, len(batch)), args...); err != nil {
			return fmt.Errorf("upsert geo objects %d-%d: %w", start, end-1, err)
		}
	}

	return nil
}

// DeleteMapObjects deletes every previously-synced row for mapUUID, across
// all versions — called when a map is removed from configuration entirely
// (unlike pruneMissingRows, which only trims rows a still-configured map's
// latest fetch no longer reports). Returns the number of rows deleted. A
// no-op (0, nil) if FieldMapUUID is skipped in the column mapping, since
// there'd be nothing to scope the delete by.
func (s *Store) DeleteMapObjects(ctx context.Context, mapUUID string) (int64, error) {
	mapUUIDCol := s.col(config.FieldMapUUID)
	if mapUUIDCol == "" {
		return 0, nil
	}

	//nolint:gosec // s.table/mapUUIDCol come from trusted server-side config, not request input; value is bound via '?'
	stmt := fmt.Sprintf("DELETE FROM %s WHERE %s = ?;", s.table, mapUUIDCol)

	res, err := s.db.ExecContext(ctx, stmt, mapUUID)
	if err != nil {
		return 0, fmt.Errorf("delete geo objects for map %s: %w", mapUUID, err)
	}

	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("delete geo objects for map %s: %w", mapUUID, err)
	}

	return n, nil
}

// pruneMissingRows deletes rows previously stored for mapUUID+version whose
// uuid is not in keepUUIDs, i.e. objects tileserve-go no longer reports for
// that map/version. An empty keepUUIDs (the map/version's fetch returned no
// objects at all) deletes every row in that scope.
func (s *Store) pruneMissingRows(ctx context.Context, tx *sql.Tx, mapUUID, version string, keepUUIDs []string) error {
	mapUUIDCol := s.col(config.FieldMapUUID)
	versionCol := s.col(config.FieldVersion)
	uuidCol := s.col(config.FieldUUID)

	args := []any{mapUUID, version}

	var stmt string

	if len(keepUUIDs) == 0 {
		stmt = fmt.Sprintf("DELETE FROM %s WHERE %s = ? AND %s = ?;", s.table, mapUUIDCol, versionCol)
	} else {
		placeholders := strings.TrimSuffix(strings.Repeat("?, ", len(keepUUIDs)), ", ")
		stmt = fmt.Sprintf("DELETE FROM %s WHERE %s = ? AND %s = ? AND %s NOT IN (%s);",
			s.table, mapUUIDCol, versionCol, uuidCol, placeholders)

		for _, u := range keepUUIDs {
			args = append(args, u)
		}
	}

	res, err := tx.ExecContext(ctx, stmt, args...)
	if err != nil {
		return fmt.Errorf("prune missing geo objects for map %s version %s: %w", mapUUID, version, err)
	}

	if n, err := res.RowsAffected(); err == nil && n > 0 {
		log.Printf("pruned %d missing geo object(s) for map %s version %s", n, mapUUID, version)
	}

	return nil
}
