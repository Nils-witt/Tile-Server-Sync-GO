package configdb

import (
	"context"
	"fmt"
	"strings"

	"gorm.io/gorm"
)

// configScalar is the singleton row (id=1) holding config.Config's non-list
// scalar fields (API/Database). It has no exported use outside this
// package — Store.Load/Save translate to/from config.Config instead.
type configScalar struct {
	ID          int64  `gorm:"column:id;primaryKey;autoIncrement:false;check:cfg_scalar_singleton,id = 1"`
	APIBaseURL  string `gorm:"column:api_base_url;not null;default:''"`
	APIUsername string `gorm:"column:api_username;not null;default:''"`
	APIPassword string `gorm:"column:api_password;not null;default:''"`
	APIToken    string `gorm:"column:api_token;not null;default:''"`
	// DBDSN is the pre-split connection string, only read to migrate an
	// older database (see Load); Save always clears it.
	DBDSN           string `gorm:"column:db_dsn;not null;default:''"`
	DBHost          string `gorm:"column:db_host;not null;default:''"`
	DBPort          int    `gorm:"column:db_port;not null;default:0"`
	DBUser          string `gorm:"column:db_user;not null;default:''"`
	DBPassword      string `gorm:"column:db_password;not null;default:''"`
	DBName          string `gorm:"column:db_name;not null;default:''"`
	DBParams        string `gorm:"column:db_params;not null;default:''"`
	DBTLS           bool   `gorm:"column:db_tls;not null;default:false"`
	DBTLSSkipVerify bool   `gorm:"column:db_tls_skip_verify;not null;default:false"`
	DBTLSCACert     string `gorm:"column:db_tls_ca_cert;not null;default:''"`
	DBTable         string `gorm:"column:db_table;not null;default:''"`
	DBPruneMissing  bool   `gorm:"column:db_prune_missing;not null;default:false"`
	DBSyncOverlays  bool   `gorm:"column:db_sync_overlays;not null;default:false"`
}

// TableName pins this model to a singular name — GORM would otherwise
// pluralize "configScalar" to "config_scalars".
func (configScalar) TableName() string { return "config_scalar" }

// databaseColumn is one config.Database.Columns entry (a GeoObject field to
// target-table column override).
type databaseColumn struct {
	Field  string `gorm:"column:field;primaryKey"`
	Column string `gorm:"column:column;not null"`
}

func (databaseColumn) TableName() string { return "database_columns" }

// mapRecord is one configured map (config.MapTarget), keyed internally by
// its own autoincrement ID (MapID is the business identifier used by the
// REST/CLI layer — see maps.go's Get/Create/Update/DeleteMap). Versions and
// StaticColumns are declared as GORM has-many associations purely so
// Preload (load.go/maps.go) can populate them in one query; their
// "constraint:-" deliberately opts them out of GORM's own foreign-key
// management — see fkTableStatements below for why.
type mapRecord struct {
	ID            int64             `gorm:"column:id;primaryKey;autoIncrement"`
	MapID         string            `gorm:"column:map_id;not null;uniqueIndex:idx_maps_map_id"`
	Name          string            `gorm:"column:name;not null;default:''"`
	SortOrder     int               `gorm:"column:sort_order;not null"`
	Interval      string            `gorm:"column:interval;not null;default:''"`
	Disabled      bool              `gorm:"column:disabled;not null;default:false"`
	Versions      []mapVersion      `gorm:"foreignKey:MapRowID;references:ID;constraint:-"`
	StaticColumns []mapStaticColumn `gorm:"foreignKey:MapRowID;references:ID;constraint:-"`
}

func (mapRecord) TableName() string { return "maps" }

// mapVersion is one version string configured for a map. MapRowID is
// intentionally not named "MapID" — that name is already taken by
// mapRecord's own business identifier, and the two must never be confused
// (this column holds mapRecord.ID, not mapRecord.MapID).
type mapVersion struct {
	ID        int64  `gorm:"column:id;primaryKey;autoIncrement"`
	MapRowID  int64  `gorm:"column:map_id;not null;index:idx_map_versions_map_id"`
	Version   string `gorm:"column:version;not null"`
	SortOrder int    `gorm:"column:sort_order;not null"`
}

func (mapVersion) TableName() string { return "map_versions" }

// mapStaticColumn is one config.MapTarget.StaticColumns entry.
type mapStaticColumn struct {
	ID       int64  `gorm:"column:id;primaryKey;autoIncrement"`
	MapRowID int64  `gorm:"column:map_id;not null;uniqueIndex:idx_map_static_columns_unique"`
	Column   string `gorm:"column:column;not null;uniqueIndex:idx_map_static_columns_unique"`
	Value    string `gorm:"column:value;not null"`
}

func (mapStaticColumn) TableName() string { return "map_static_columns" }

// fkTableStatements creates the two tables above that need a real,
// DB-enforced foreign key with ON DELETE CASCADE (map_versions/
// map_static_columns -> maps). SQLite can
// only declare a foreign key at CREATE TABLE time — ALTER TABLE ADD
// CONSTRAINT isn't supported — so these are created directly via raw SQL
// (CREATE TABLE IF NOT EXISTS, a no-op after the first run) rather than
// through GORM's association/constraint tags, and every association above
// pointing at these tables opts out via "constraint:-".
//
// The reason isn't just style: on a database created by an older, hand-
// written-SQL version of this schema, letting GORM manage (verify/recreate)
// one of these foreign keys makes it try to recreate the table — SQLite's
// only way to add a constraint to an existing table — and the sqlite
// driver's table-recreate DDL parser mishandles map_static_columns'
// composite UNIQUE(map_id, column) table constraint together with its
// "column" column (a SQL reserved word), corrupting the copy. Owning these
// two CREATE TABLEs ourselves sidesteps that recreate path entirely, on
// both a fresh install and an upgrade from the old schema. AutoMigrate (see
// migrate below) still runs over them afterward to backfill any missing
// column/index — safe operations that never require a recreate.
var fkTableStatements = []string{
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
		value  TEXT NOT NULL
	)`,
}

// legacyInlineUniqueTables lists, for each table an older raw-SQL version of
// this schema declared with an inline composite "UNIQUE(a, b)" table
// constraint, the replacement CREATE TABLE (without that constraint — see
// fkTableStatements, this is one of the same tables) and the explicit column
// list to carry over. dropLegacyInlineUniqueConstraint below only touches a
// table whose stored DDL still contains that constraint, so this is a
// no-op on a fresh install or an already-normalized database.
var legacyInlineUniqueTables = []struct {
	table, createSQL, columns string
}{
	{
		table: "map_static_columns",
		createSQL: `CREATE TABLE map_static_columns (
			id     INTEGER PRIMARY KEY AUTOINCREMENT,
			map_id INTEGER NOT NULL REFERENCES maps(id) ON DELETE CASCADE,
			column TEXT NOT NULL,
			value  TEXT NOT NULL
		)`,
		columns: "id, map_id, column, value",
	},
}

// dropLegacyInlineUniqueConstraint rewrites one legacyInlineUniqueTables
// entry in place (rename-swap, with an explicit column list rather than
// "SELECT *") if — and only if — its stored DDL still has the old inline
// UNIQUE constraint. This exists solely to get out from under a sqlite
// driver bug: asking GORM itself to reconcile that constraint (via
// AlterColumn/CreateConstraint/DropConstraint, all of which recreate the
// table — SQLite has no ALTER TABLE ADD/DROP CONSTRAINT) makes its DDL
// parser misparse the trailing "UNIQUE(a, b)" as a column named UNIQUE,
// corrupting the data copy (reproduced and confirmed against
// github.com/glebarez/sqlite v1.11.0). Doing the rewrite ourselves with a
// known-good column list sidesteps that parser entirely. Once rewritten,
// AutoMigrate (see migrate below) only ever needs to add the replacement
// named unique index — a plain CREATE INDEX, not a recreate.
func (s *Store) dropLegacyInlineUniqueConstraint(ctx context.Context, table, createSQL, columns string) error {
	db := s.db.WithContext(ctx)

	var ddl string
	if err := db.Raw(
		`SELECT sql FROM sqlite_master WHERE type = 'table' AND name = ?`, table,
	).Scan(&ddl).Error; err != nil {
		return fmt.Errorf("inspect table %q: %w", table, err)
	}

	if !strings.Contains(ddl, "UNIQUE(") {
		return nil
	}

	tmpTable := table + "__legacy_unique_rewrite"

	return db.Transaction(func(tx *gorm.DB) error {
		for _, stmt := range []string{
			"DROP TABLE IF EXISTS " + tmpTable,
			flatDDL(strings.Replace(createSQL, table, tmpTable, 1)),
			fmt.Sprintf("INSERT INTO %s (%s) SELECT %s FROM %s", tmpTable, columns, columns, table),
			"DROP TABLE " + table,
			"ALTER TABLE " + tmpTable + " RENAME TO " + table,
		} {
			if err := tx.Exec(stmt).Error; err != nil {
				return fmt.Errorf("rewrite legacy table %q: %w", table, err)
			}
		}

		return nil
	})
}

// flatDDL collapses a hand-written statement's whitespace to single spaces
// before it's executed. SQLite stores CREATE TABLE text verbatim, and
// glebarez/sqlite's DDL parser (used by AutoMigrate to diff and recreate a
// table) treats a tab as a quote character, so tab-indented DDL makes it
// misread every column after the first tab: it then skips type changes or
// corrupts the column list when copying rows into a recreated table.
func flatDDL(stmt string) string {
	return strings.Join(strings.Fields(stmt), " ")
}

// migrate creates/updates every table this package owns. It first drops
// the obsolete local-account and SSO-settings tables (see
// removeObsoleteTables); the table
// fkTableStatements' foreign keys point at must then exist first (maps); then legacyInlineUniqueTables' one-time rewrite (see
// dropLegacyInlineUniqueConstraint); then fkTableStatements itself; then
// GORM's AutoMigrate over every model, which is idempotent (safe on every
// startup) and — for a database created by an older raw-SQL version of this
// schema — smart enough to add missing columns/indexes and to widen a
// column whose declared type no longer matches (e.g. the old plain-INTEGER
// boolean/TEXT timestamp columns), without touching the data already
// stored in them.
func (s *Store) migrate(ctx context.Context) error {
	db := s.db.WithContext(ctx)

	if err := removeObsoleteTables(db); err != nil {
		return err
	}

	if err := db.AutoMigrate(&mapRecord{}); err != nil {
		return fmt.Errorf("migrate config schema: %w", err)
	}

	for _, t := range legacyInlineUniqueTables {
		if err := s.dropLegacyInlineUniqueConstraint(ctx, t.table, t.createSQL, t.columns); err != nil {
			return fmt.Errorf("migrate config schema: %w", err)
		}
	}

	for _, stmt := range fkTableStatements {
		if err := db.Exec(flatDDL(stmt)).Error; err != nil {
			return fmt.Errorf("migrate config schema: %w", err)
		}
	}

	if err := db.AutoMigrate(
		&configScalar{},
		&databaseColumn{},
		&mapRecord{},
		&mapVersion{},
		&mapStaticColumn{},
		&SecurityLogEntry{},
	); err != nil {
		return fmt.Errorf("migrate config schema: %w", err)
	}

	return nil
}

// removeObsoleteTables drops tables an older database still has but nothing
// reads any more: users (with their own permission grants and, before
// that, password hashes), plus the sessions and sso_identities rows hanging
// off it — every login is SSO now and every permission comes from the
// token's groups (see internal/webserver/sso_bearer.go) — and sso_config,
// since the SSO settings moved to the bootstrap file. Children go first; this runs before Open enforces foreign
// keys anyway. A no-op on a fresh or already-cleaned database.
func removeObsoleteTables(db *gorm.DB) error {
	for _, table := range []string{"sessions", "sso_identities", "users", "sso_config"} {
		if err := db.Exec("DROP TABLE IF EXISTS " + table).Error; err != nil {
			return fmt.Errorf("migrate config schema: drop %s: %w", table, err)
		}
	}

	return nil
}
