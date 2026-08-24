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
	ID       string   `yaml:"id"                 json:"id"`
	Versions []string `yaml:"versions"           json:"versions"`
	// StaticColumns maps extra target table column names onto fixed values
	// written to every row synced from this map (e.g. a "source" or
	// "region" tag). These columns are in addition to the ones GeoObject
	// fields map onto via Database.Columns and are created automatically
	// (as VARCHAR(255) NOT NULL DEFAULT '') by EnsureSchema.
	StaticColumns map[string]string `yaml:"staticColumns" json:"staticColumns"`
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

// Config is the root configuration document.
type Config struct {
	API       API         `yaml:"api"       json:"api"`
	Database  Database    `yaml:"database"  json:"database"`
	Maps      []MapTarget `yaml:"maps"      json:"maps"`
	WebServer WebServer   `yaml:"webServer" json:"webServer"`
	// Interval, if set, is a Go duration string (e.g. "5m", "1h") for how
	// often to repeat the full sync. If empty, the sync runs once and exits.
	Interval string `yaml:"interval" json:"interval"`
	// interval is Interval parsed by validate(); read it via SyncInterval.
	interval time.Duration
}

const defaultWebServerAddress = ":8080"

// SyncInterval returns the parsed Interval, or 0 if none was configured,
// meaning: run the sync once and exit.
func (c *Config) SyncInterval() time.Duration {
	return c.interval
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
// (e.g. one assembled by the web config editor, or a file already read by
// the caller), applying the same defaulting and validation Load does. On
// success, every field left unset in data is filled with its default value.
func Parse(data []byte) (*Config, error) {
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}

	return &cfg, nil
}

func (c *Config) validate() error {
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

	if err := c.validateInterval(); err != nil {
		return err
	}

	if c.WebServer.Enabled && c.WebServer.Address == "" {
		c.WebServer.Address = defaultWebServerAddress
	}

	return c.validateMaps()
}

// validateInterval parses Interval (if set) and stores the result in
// interval for SyncInterval to return.
func (c *Config) validateInterval() error {
	if c.Interval == "" {
		return nil
	}

	d, err := time.ParseDuration(c.Interval)
	if err != nil {
		return fmt.Errorf("interval %q is not a valid duration: %w", c.Interval, err)
	}

	if d <= 0 {
		return fmt.Errorf("interval %q must be positive", c.Interval)
	}

	c.interval = d

	return nil
}

// validateMaps checks every maps[] entry, including that each map's
// staticColumns are valid SQL identifiers that don't collide with a
// database.columns target (split out from validate to keep its cyclomatic
// complexity down).
func (c *Config) validateMaps() error {
	reservedCols := make(map[string]bool, len(c.Database.Columns))
	for _, col := range c.Database.Columns {
		if col != "" {
			reservedCols[col] = true
		}
	}

	for i, m := range c.Maps {
		if m.ID == "" {
			return fmt.Errorf("maps[%d].id is required", i)
		}

		if len(m.Versions) == 0 {
			return fmt.Errorf("maps[%d].versions must contain at least one version", i)
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
