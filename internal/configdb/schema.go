package configdb

import (
	"context"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
)

// configScalar is the singleton row (id=1) holding config.Config's non-list
// scalar fields (API/Database). It has no exported use outside this
// package — Store.Load/Save translate to/from config.Config instead.
type configScalar struct {
	ID             int64  `gorm:"column:id;primaryKey;autoIncrement:false;check:cfg_scalar_singleton,id = 1"`
	APIBaseURL     string `gorm:"column:api_base_url;not null;default:''"`
	APIUsername    string `gorm:"column:api_username;not null;default:''"`
	APIPassword    string `gorm:"column:api_password;not null;default:''"`
	APIToken       string `gorm:"column:api_token;not null;default:''"`
	DBDSN          string `gorm:"column:db_dsn;not null;default:''"`
	DBTable        string `gorm:"column:db_table;not null;default:''"`
	DBPruneMissing bool   `gorm:"column:db_prune_missing;not null;default:false"`
	DBSyncOverlays bool   `gorm:"column:db_sync_overlays;not null;default:false"`
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

// session is one issued login session (see users.go's CreateSession/
// SessionUser). User is a pointer purely so Preload could populate it if
// ever needed; it's never actually populated or read — every session row is
// created/queried by UserID/TokenHash directly. Its "constraint:-" opts it
// out of GORM's own foreign-key management, same as mapRecord's
// associations above.
type session struct {
	TokenHash string    `gorm:"column:token_hash;primaryKey"`
	UserID    int64     `gorm:"column:user_id;not null;index:idx_sessions_user_id"`
	User      *User     `gorm:"constraint:-"`
	CreatedAt time.Time `gorm:"column:created_at;not null"`
	ExpiresAt time.Time `gorm:"column:expires_at;not null"`
}

func (session) TableName() string { return "sessions" }

// ssoIdentity links a verified OIDC (issuer, subject) pair to a local user
// (see sso.go's FindOrCreateSSOUser). User is a pointer for the same reason
// as session.User above.
type ssoIdentity struct {
	ID        int64     `gorm:"column:id;primaryKey;autoIncrement"`
	Issuer    string    `gorm:"column:issuer;not null;uniqueIndex:idx_sso_identities_unique"`
	Subject   string    `gorm:"column:subject;not null;uniqueIndex:idx_sso_identities_unique"`
	UserID    int64     `gorm:"column:user_id;not null;index:idx_sso_identities_user_id"`
	User      *User     `gorm:"constraint:-"`
	CreatedAt time.Time `gorm:"column:created_at;not null"`
}

func (ssoIdentity) TableName() string { return "sso_identities" }

// fkTableStatements creates the four tables above that need a real,
// DB-enforced foreign key with ON DELETE CASCADE (map_versions/
// map_static_columns -> maps, sessions/sso_identities -> users). SQLite can
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
// four CREATE TABLEs ourselves sidesteps that recreate path entirely, on
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
	`CREATE TABLE IF NOT EXISTS sessions (
		token_hash TEXT PRIMARY KEY,
		user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		created_at TEXT NOT NULL,
		expires_at TEXT NOT NULL
	)`,
	`CREATE TABLE IF NOT EXISTS sso_identities (
		id         INTEGER PRIMARY KEY AUTOINCREMENT,
		issuer     TEXT NOT NULL,
		subject    TEXT NOT NULL,
		user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		created_at TEXT NOT NULL
	)`,
}

// legacyInlineUniqueTables lists, for each table an older raw-SQL version of
// this schema declared with an inline composite "UNIQUE(a, b)" table
// constraint, the replacement CREATE TABLE (without that constraint — see
// fkTableStatements, these are the same two tables) and the explicit column
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
	{
		table: "sso_identities",
		createSQL: `CREATE TABLE sso_identities (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			issuer     TEXT NOT NULL,
			subject    TEXT NOT NULL,
			user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			created_at TEXT NOT NULL
		)`,
		columns: "id, issuer, subject, user_id, created_at",
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

// usersRebuildColumns is every users column the User model maps, in
// rebuildLegacyUsersTable's copy order.
const usersRebuildColumns = "id, username, password_hash, is_superuser, perm_view_status, perm_trigger_sync, " +
	"perm_view_config, perm_edit_config_api, perm_edit_config_database, perm_edit_config_maps, created_at"

// rebuildLegacyUsersTable recreates users when its created_at column is
// still declared TEXT. The sqlite driver only returns time.Time for
// date-typed columns, so a TEXT one fails every user load with a Scan
// error. AutoMigrate normally widens it, but not when the stored DDL still
// contains a tab (see flatDDL) — e.g. the old tab-indented CREATE TABLE
// with a column later appended by ALTER TABLE ADD COLUMN — in which case it
// misreads created_at's type and silently skips it. A users table whose DDL
// still has a tab is rebuilt too, so later AutoMigrate runs can parse it.
// Columns the model no longer maps (perm_edit_config_sso) are dropped.
//
// users is the parent of sessions and sso_identities; this relies on
// migrate running before Open enforces foreign keys, so DROP TABLE doesn't
// cascade to them. They reference users by name and resolve to the
// rebuilt table once it is renamed.
func (s *Store) rebuildLegacyUsersTable(ctx context.Context) error {
	db := s.db.WithContext(ctx)

	var colType, ddl string
	if err := db.Raw(
		`SELECT type FROM pragma_table_info('users') WHERE name = 'created_at'`,
	).Scan(&colType).Error; err != nil {
		return fmt.Errorf("inspect users table: %w", err)
	}

	if err := db.Raw(
		`SELECT sql FROM sqlite_master WHERE type = 'table' AND name = 'users'`,
	).Scan(&ddl).Error; err != nil {
		return fmt.Errorf("inspect users table: %w", err)
	}

	if colType == "" || (strings.EqualFold(colType, "datetime") && !strings.Contains(ddl, "\t")) {
		return nil
	}

	return db.Transaction(func(tx *gorm.DB) error {
		for _, stmt := range []string{
			"DROP TABLE IF EXISTS users__rebuild",
			`CREATE TABLE users__rebuild (
				id                        INTEGER PRIMARY KEY AUTOINCREMENT,
				username                  TEXT NOT NULL,
				password_hash             TEXT NOT NULL,
				is_superuser              numeric NOT NULL DEFAULT false,
				perm_view_status          numeric,
				perm_trigger_sync         numeric,
				perm_view_config          numeric,
				perm_edit_config_api      numeric,
				perm_edit_config_database numeric,
				perm_edit_config_maps     numeric,
				created_at                datetime NOT NULL
			)`,
			"INSERT INTO users__rebuild (" + usersRebuildColumns + ") SELECT " + usersRebuildColumns + " FROM users",
			"DROP TABLE users",
			"ALTER TABLE users__rebuild RENAME TO users",
		} {
			if err := tx.Exec(flatDDL(stmt)).Error; err != nil {
				return fmt.Errorf("rebuild users table: %w", err)
			}
		}

		return nil
	})
}

// migrate creates/updates every table this package owns. It runs in four
// passes: the tables fkTableStatements' foreign keys point at must exist
// first (maps, users); then legacyInlineUniqueTables' one-time rewrite (see
// dropLegacyInlineUniqueConstraint); then fkTableStatements itself; then
// GORM's AutoMigrate over every model, which is idempotent (safe on every
// startup) and — for a database created by an older raw-SQL version of this
// schema — smart enough to add missing columns/indexes and to widen a
// column whose declared type no longer matches (e.g. the old plain-INTEGER
// boolean/TEXT timestamp columns), without touching the data already
// stored in them.
func (s *Store) migrate(ctx context.Context) error {
	db := s.db.WithContext(ctx)

	if err := db.AutoMigrate(&mapRecord{}, &User{}); err != nil {
		return fmt.Errorf("migrate config schema: %w", err)
	}

	if err := s.rebuildLegacyUsersTable(ctx); err != nil {
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
		&User{},
		&session{},
		&ssoIdentity{},
		&SecurityLogEntry{},
	); err != nil {
		return fmt.Errorf("migrate config schema: %w", err)
	}

	return nil
}
