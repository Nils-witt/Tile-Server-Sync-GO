// Package config loads the application's YAML configuration file.
package config

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"sort"
	"time"

	"gopkg.in/yaml.v3"
)

// Field keys usable in database.columns to map a GeoObject field (or the
// synced_at bookkeeping column) onto a target table column name.
const (
	FieldUUID         = "uuid"
	FieldMapUUID      = "mapUuid"
	FieldVersion      = "version"
	FieldName         = "name"
	FieldExternalID   = "externalId"
	FieldLatitude     = "latitude"
	FieldLongitude    = "longitude"
	FieldStreet       = "street"
	FieldHousenumber  = "housenumber"
	FieldPostcode     = "postcode"
	FieldCity         = "city"
	FieldCityDistrict = "cityDistrict"
	FieldCreatedAt    = "createdAt"
	FieldUpdatedAt    = "updatedAt"
	FieldCreatedBy    = "createdBy"
	FieldUpdatedBy    = "updatedBy"
	// FieldSyncedAt is not part of GeoObject; it's the bookkeeping column
	// that records when a row was last synced.
	FieldSyncedAt = "syncedAt"
)

const defaultTable = "geo_objects"

var defaultColumns = map[string]string{
	FieldUUID:         "uuid",
	FieldMapUUID:      "map_uuid",
	FieldVersion:      "version",
	FieldName:         "name",
	FieldExternalID:   "external_id",
	FieldLatitude:     "latitude",
	FieldLongitude:    "longitude",
	FieldStreet:       "street",
	FieldHousenumber:  "housenumber",
	FieldPostcode:     "postcode",
	FieldCity:         "city",
	FieldCityDistrict: "city_district",
	FieldCreatedAt:    "created_at",
	FieldUpdatedAt:    "updated_at",
	FieldCreatedBy:    "created_by",
	FieldUpdatedBy:    "updated_by",
	FieldSyncedAt:     "synced_at",
}

var identifierPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// MapTarget names one map and the version(s) of it whose geo objects should
// be synced. Version may be a real numeric version, the literal "current",
// or a user-defined alias.
type MapTarget struct {
	ID string `yaml:"id" json:"id"`
	// Name is a human-readable label for the map, distinct from its ID
	// (a UUID). Optional for the map itself, but required for it to be
	// synced to EDP (see Database.SyncOverlays): a map with SyncOverlays
	// enabled and no Name is skipped, logged, rather than failing the sync.
	Name     string   `yaml:"name"     json:"name"`
	Versions []string `yaml:"versions" json:"versions"`
	// StaticColumns maps extra target table column names onto fixed values
	// written to every row synced from this map (e.g. a "source" or
	// "region" tag). These columns are in addition to the ones GeoObject
	// fields map onto via Database.Columns and are created automatically
	// (as VARCHAR(255) NOT NULL DEFAULT '') by EnsureSchema.
	StaticColumns map[string]string `yaml:"staticColumns" json:"staticColumns"`
	// Interval, if set, is a Go duration string (e.g. "5m", "1h") for how
	// often this map re-syncs. If empty, this map is synced once (at
	// startup, or once picked up by a live config reload) and not
	// automatically repeated — other maps with their own Interval keep
	// repeating on their own schedule regardless. See runLoop in main.go.
	Interval string `yaml:"interval" json:"interval"`
	// interval is Interval parsed by validateInterval(); read it via
	// SyncInterval.
	interval time.Duration
}

// SyncInterval returns m's parsed Interval, or 0 if none was configured,
// meaning: sync this map once and don't automatically repeat.
func (m *MapTarget) SyncInterval() time.Duration {
	return m.interval
}

// API holds connection details for the tileserve-go instance.
type API struct {
	BaseURL  string `yaml:"baseUrl"  json:"baseUrl"`
	Username string `yaml:"username" json:"username"`
	Password string `yaml:"password" json:"password"`
	// Token, if set, is used directly instead of logging in with
	// Username/Password.
	Token string `yaml:"token" json:"token"`
}

// Database holds the MariaDB connection string (Go MySQL driver DSN, e.g.
// "user:pass@tcp(127.0.0.1:3306)/dbname?parseTime=true") plus where and how
// synced geo objects are written.
type Database struct {
	DSN string `yaml:"dsn" json:"dsn"`
	// Table is the target table name. Defaults to "geo_objects".
	Table string `yaml:"table" json:"table"`
	// Columns maps GeoObject field keys (see FieldUUID etc.) and
	// FieldSyncedAt onto target table column names. Any field omitted from
	// the map uses its default column name. A field explicitly mapped to ""
	// is skipped entirely, letting sync target an existing table (e.g. one
	// with a different, narrower schema) that only has some of the columns.
	// FieldUUID may not be skipped: it's the key UpsertGeoObjects matches
	// existing rows on.
	Columns map[string]string `yaml:"columns" json:"columns"`
	// PruneMissing, if true, deletes rows belonging to a synced map/version
	// whose uuid was not present in that sync's fetch response, i.e. objects
	// that have been removed on the tileserve-go side since the last sync.
	// Pruning is scoped per map_uuid+version (the version column holds the
	// configured version string, e.g. "current", not whatever concrete
	// version the API resolved it to — see main.go); a fetch that returns
	// zero objects prunes every row in that map/version's scope. Requires
	// Columns["mapUuid"] and Columns["version"] to both be set (not skipped).
	PruneMissing bool `yaml:"pruneMissing" json:"pruneMissing"`
	// SyncOverlays, if true, keeps a row per configured map/version in the
	// same MariaDB database's map_src_overlays table (an external
	// application's overlay list, e.g. EDP) in sync with maps created,
	// updated, or deleted through this tool — see internal/store's
	// CreateMapOverlays/UpdateMapOverlays/DeleteMapOverlays. Off by default
	// so deployments without that table are unaffected.
	SyncOverlays bool `yaml:"syncOverlays" json:"syncOverlays"`
}

// WebServer configures the optional HTTP server that exposes sync status and
// recent log output.
type WebServer struct {
	Enabled bool `yaml:"enabled" json:"enabled"`
	// Address is the address (see net/http.Server.Addr) the server listens
	// on, e.g. ":8080" or "127.0.0.1:8080". Defaults to ":8080" if enabled
	// and left empty.
	Address string `yaml:"address" json:"address"`
}

// applyDefault fills Address with defaultWebServerAddress if Enabled and
// Address is empty. Shared by Config.Validate and LoadBootstrap, since
// WebServer is now validated/defaulted in two different places (a full
// Config, and the standalone bootstrap file).
func (w *WebServer) applyDefault() {
	if w.Enabled && w.Address == "" {
		w.Address = defaultWebServerAddress
	}
}

// Config is the root configuration document.
type Config struct {
	API       API         `yaml:"api"       json:"api"`
	Database  Database    `yaml:"database"  json:"database"`
	Maps      []MapTarget `yaml:"maps"      json:"maps"`
	WebServer WebServer   `yaml:"webServer" json:"webServer"`
}

const defaultWebServerAddress = ":8080"

// HasRecurringMaps reports whether any configured map has a positive
// Interval. If false, every configured map is one-shot, so a caller that
// only runs once when there's nothing to repeat (see main.go's run, when
// webServer is disabled) knows it can sync once and exit rather than loop.
func (c *Config) HasRecurringMaps() bool {
	for _, m := range c.Maps {
		if m.SyncInterval() > 0 {
			return true
		}
	}

	return false
}

// Load reads and parses the YAML config file at path. path is a
// user-supplied CLI flag naming a local config file, not untrusted input
// from a remote caller.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path) //nolint:gosec // path is a trusted, user-supplied CLI flag
	if err != nil {
		return nil, fmt.Errorf("read config %q: %w", path, err)
	}

	cfg, err := Parse(data)
	if err != nil {
		return nil, fmt.Errorf("%w (from %q)", err, path)
	}

	return cfg, nil
}

// Parse unmarshals and validates a YAML config document already in memory
// (e.g. one already read by the caller), applying the same defaulting and
// validation Load does. On success, every field left unset in data is
// filled with its default value.
func Parse(data []byte) (*Config, error) {
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}

	return &cfg, nil
}

// Validate checks c for consistency, filling in defaults (Database.Table,
// Database.Columns, WebServer.Address) as it goes. Callers that assemble a
// *Config from a source other than Parse (configdb, plus the WebServer
// overlay applied by runtime.reload) must call this themselves before using
// the result.
func (c *Config) Validate() error {
	if c.API.BaseURL == "" {
		return errors.New("api.baseUrl is required")
	}

	if c.API.Token == "" && (c.API.Username == "" || c.API.Password == "") {
		return errors.New("either api.token or both api.username and api.password are required")
	}

	if c.Database.DSN == "" {
		return errors.New("database.dsn is required")
	}

	if err := c.Database.validate(); err != nil {
		return err
	}

	if len(c.Maps) == 0 {
		return errors.New("at least one entry under maps is required")
	}

	c.WebServer.applyDefault()

	return c.ValidateMaps()
}

// ValidateMaps checks every maps[] entry — including parsing its Interval
// (storing the result for SyncInterval to return), checking that its
// staticColumns are valid SQL identifiers that don't collide with a
// database.columns target, and that no two entries share an id — split out
// from Validate to keep its cyclomatic complexity down, and exported so
// callers that validate just a candidate maps list (e.g. the webserver's
// per-map create/update handlers, which don't have api/database.dsn filled in
// to satisfy the rest of Validate) can call it directly against
// c.Maps/c.Database.Columns alone.
func (c *Config) ValidateMaps() error {
	reservedCols := make(map[string]bool, len(c.Database.Columns))
	for _, col := range c.Database.Columns {
		if col != "" {
			reservedCols[col] = true
		}
	}

	seenIDs := make(map[string]bool, len(c.Maps))

	for i := range c.Maps {
		m := &c.Maps[i]

		if m.ID == "" {
			return fmt.Errorf("maps[%d].id is required", i)
		}

		if seenIDs[m.ID] {
			return fmt.Errorf("maps[%d].id %q is duplicated", i, m.ID)
		}

		seenIDs[m.ID] = true

		if len(m.Versions) == 0 {
			return fmt.Errorf("maps[%d].versions must contain at least one version", i)
		}

		if err := m.validateInterval(i); err != nil {
			return err
		}

		for col := range m.StaticColumns {
			if !identifierPattern.MatchString(col) {
				return fmt.Errorf("maps[%d].staticColumns has invalid column name %q", i, col)
			}

			if reservedCols[col] {
				return fmt.Errorf("maps[%d].staticColumns[%q] collides with a database.columns target", i, col)
			}
		}
	}

	return nil
}

// validateInterval parses m.Interval (if set) and stores the result in
// m.interval for SyncInterval to return. i is the map's index in Config.Maps,
// used only to name it in an error.
func (m *MapTarget) validateInterval(i int) error {
	if m.Interval == "" {
		return nil
	}

	d, err := time.ParseDuration(m.Interval)
	if err != nil {
		return fmt.Errorf("maps[%d].interval %q is not a valid duration: %w", i, m.Interval, err)
	}

	if d <= 0 {
		return fmt.Errorf("maps[%d].interval %q must be positive", i, m.Interval)
	}

	m.interval = d

	return nil
}

// StaticColumnNames returns the sorted, de-duplicated set of column names
// referenced by any map's staticColumns. store.EnsureSchema uses it to
// create the extra columns and store.UpsertGeoObjects uses it to fix the
// column order used when binding per-map static values.
func (c *Config) StaticColumnNames() []string {
	set := make(map[string]struct{})

	for _, m := range c.Maps {
		for col := range m.StaticColumns {
			set[col] = struct{}{}
		}
	}

	names := make([]string, 0, len(set))
	for col := range set {
		names = append(names, col)
	}

	sort.Strings(names)

	return names
}

// validate fills in defaults for Table and any unset Columns entries, then
// checks that Table and every non-skipped column name is a safe SQL
// identifier (they're interpolated directly into generated SQL rather than
// bound as parameters, since the driver can't parameterize identifiers).
func (d *Database) validate() error {
	if d.Table == "" {
		d.Table = defaultTable
	}

	if !identifierPattern.MatchString(d.Table) {
		return fmt.Errorf("database.table %q is not a valid SQL identifier", d.Table)
	}

	if err := d.validateColumns(); err != nil {
		return err
	}

	if d.Columns[FieldUUID] == "" {
		return fmt.Errorf("database.columns[%q] may not be skipped: it's the upsert key", FieldUUID)
	}

	if d.PruneMissing && (d.Columns[FieldMapUUID] == "" || d.Columns[FieldVersion] == "") {
		return fmt.Errorf("database.pruneMissing requires database.columns[%q] and database.columns[%q] to be set",
			FieldMapUUID, FieldVersion)
	}

	return nil
}

// validateColumns checks any explicitly configured Columns entries, then
// fills in defaults for every field left unset (split out from validate to
// keep its cyclomatic complexity down).
func (d *Database) validateColumns() error {
	if d.Columns == nil {
		d.Columns = make(map[string]string, len(defaultColumns))
	}

	for field, col := range d.Columns {
		if _, known := defaultColumns[field]; !known {
			return fmt.Errorf("database.columns has unknown field %q", field)
		}

		if col != "" && !identifierPattern.MatchString(col) {
			return fmt.Errorf("database.columns[%q] = %q is not a valid SQL identifier", field, col)
		}
	}

	for field, def := range defaultColumns {
		if _, set := d.Columns[field]; !set {
			d.Columns[field] = def
		}
	}

	return nil
}
