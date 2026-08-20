// Package config loads the application's YAML configuration file.
package config

import (
	"errors"
	"fmt"
	"os"
	"regexp"

	"gopkg.in/yaml.v3"
)

// Field keys usable in database.columns to map a GeoObject field (or the
// synced_at bookkeeping column) onto a target table column name.
const (
	FieldUUID        = "uuid"
	FieldMapUUID     = "mapUuid"
	FieldVersion     = "version"
	FieldName        = "name"
	FieldExternalID  = "externalId"
	FieldLatitude    = "latitude"
	FieldLongitude   = "longitude"
	FieldStreet      = "street"
	FieldHousenumber = "housenumber"
	FieldPostcode    = "postcode"
	FieldCreatedAt   = "createdAt"
	FieldUpdatedAt   = "updatedAt"
	FieldCreatedBy   = "createdBy"
	FieldUpdatedBy   = "updatedBy"
	// FieldSyncedAt is not part of GeoObject; it's the bookkeeping column
	// that records when a row was last synced.
	FieldSyncedAt = "syncedAt"
)

const defaultTable = "geo_objects"

var defaultColumns = map[string]string{
	FieldUUID:        "uuid",
	FieldMapUUID:     "map_uuid",
	FieldVersion:     "version",
	FieldName:        "name",
	FieldExternalID:  "external_id",
	FieldLatitude:    "latitude",
	FieldLongitude:   "longitude",
	FieldStreet:      "street",
	FieldHousenumber: "housenumber",
	FieldPostcode:    "postcode",
	FieldCreatedAt:   "created_at",
	FieldUpdatedAt:   "updated_at",
	FieldCreatedBy:   "created_by",
	FieldUpdatedBy:   "updated_by",
	FieldSyncedAt:    "synced_at",
}

var identifierPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// MapTarget names one map and the version(s) of it whose geo objects should
// be synced. Version may be a real numeric version, the literal "current",
// or a user-defined alias.
type MapTarget struct {
	ID       string   `yaml:"id"`
	Versions []string `yaml:"versions"`
}

// API holds connection details for the tileserve-go instance.
type API struct {
	BaseURL  string `yaml:"baseUrl"`
	Username string `yaml:"username"`
	Password string `yaml:"password"`
	// Token, if set, is used directly instead of logging in with
	// Username/Password.
	Token string `yaml:"token"`
}

// Database holds the MariaDB connection string (Go MySQL driver DSN, e.g.
// "user:pass@tcp(127.0.0.1:3306)/dbname?parseTime=true") plus where and how
// synced geo objects are written.
type Database struct {
	DSN string `yaml:"dsn"`
	// Table is the target table name. Defaults to "geo_objects".
	Table string `yaml:"table"`
	// Columns maps GeoObject field keys (see FieldUUID etc.) and
	// FieldSyncedAt onto target table column names. Any field omitted from
	// the map uses its default column name. A field explicitly mapped to ""
	// is skipped entirely, letting sync target an existing table (e.g. one
	// with a different, narrower schema) that only has some of the columns.
	// FieldUUID may not be skipped: it's the key UpsertGeoObjects matches
	// existing rows on.
	Columns map[string]string `yaml:"columns"`
}

// Config is the root configuration document.
type Config struct {
	API      API         `yaml:"api"`
	Database Database    `yaml:"database"`
	Maps     []MapTarget `yaml:"maps"`
}

// Load reads and parses the YAML config file at path. path is a
// user-supplied CLI flag naming a local config file, not untrusted input
// from a remote caller.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path) //nolint:gosec // path is a trusted, user-supplied CLI flag
	if err != nil {
		return nil, fmt.Errorf("read config %q: %w", path, err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config %q: %w", path, err)
	}

	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("invalid config %q: %w", path, err)
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

	for i, m := range c.Maps {
		if m.ID == "" {
			return fmt.Errorf("maps[%d].id is required", i)
		}

		if len(m.Versions) == 0 {
			return fmt.Errorf("maps[%d].versions must contain at least one version", i)
		}
	}

	return nil
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

	if d.Columns[FieldUUID] == "" {
		return fmt.Errorf("database.columns[%q] may not be skipped: it's the upsert key", FieldUUID)
	}

	return nil
}
