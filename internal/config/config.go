// Package config defines the application's configuration types and their
// validation/defaulting; the small bootstrap YAML file is loaded by
// LoadBootstrap.
package config

import (
	"errors"
	"fmt"
	"maps"
	"regexp"
	"sort"
	"time"
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

// apiIDPattern restricts API IDs to what can appear unescaped in a URL path.
var apiIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// MapTarget names one map and the version(s) of it whose geo objects should
// be synced. Version may be a real numeric version, the literal "current",
// or a user-defined alias.
type MapTarget struct {
	ID string `yaml:"id" json:"id"`
	// APIID is the ID of the configured API (see Config.APIs) this map is
	// fetched from.
	APIID string `yaml:"apiId" json:"apiId"`
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
	// repeating on their own schedule regardless. See internal/runner's Run.
	Interval string `yaml:"interval" json:"interval"`
	// interval is Interval parsed by validateInterval(); read it via
	// SyncInterval.
	interval time.Duration
	// Disabled, if true, excludes this map from automatic syncing: the
	// runner's per-map scheduler never considers it due (see scheduleTick in
	// internal/runner). Its config (versions, interval, staticColumns) is
	// kept as-is — disabling is meant to be temporary, not a lighter-weight
	// delete — and it can still be synced on demand via its own "Sync"
	// button (runner.Runner.SyncMap), which is given its ID explicitly
	// rather than discovering it through scheduling. Defaults to false
	// (enabled) so an existing map, or a request that omits the field, keeps
	// syncing.
	Disabled bool `yaml:"disabled" json:"disabled"`
}

// SyncInterval returns m's parsed Interval, or 0 if none was configured,
// meaning: sync this map once and don't automatically repeat.
func (m *MapTarget) SyncInterval() time.Duration {
	return m.interval
}

// API holds connection details for one tileserve-go instance. Maps refer
// to it by ID (MapTarget.APIID).
type API struct {
	// ID identifies the API in URLs and in MapTarget.APIID; it can't be
	// changed once created.
	ID string `yaml:"id" json:"id"`
	// Name is an optional human-readable label.
	Name     string `yaml:"name"     json:"name"`
	BaseURL  string `yaml:"baseUrl"  json:"baseUrl"`
	Username string `yaml:"username" json:"username"`
	Password string `yaml:"password" json:"password"`
	// Token, if set, is used directly instead of logging in with
	// Username/Password.
	Token string `yaml:"token" json:"token"`
}

// Validate checks a on its own: a valid ID, a base URL plus either a token
// or both a username and password. Used by Config.Validate and to check one
// API alone before it's saved.
func (a *API) Validate() error {
	if !apiIDPattern.MatchString(a.ID) {
		return fmt.Errorf("api.id %q must be 1-64 letters, digits, '-' or '_'", a.ID)
	}

	if a.BaseURL == "" {
		return errors.New("api.baseUrl is required")
	}

	if a.Token == "" && (a.Username == "" || a.Password == "") {
		return errors.New("either api.token or both api.username and api.password are required")
	}

	return nil
}

// Validate checks d on its own — its connection settings
// (ValidateConnection) plus its table/column mapping — without modifying
// it: the defaults Config.Validate fills in are applied to a copy only. Used
// to check the database section alone before it's saved.
func (d *Database) Validate() error {
	if err := d.ValidateConnection(); err != nil {
		return err
	}

	cp := *d
	cp.Columns = maps.Clone(d.Columns)

	return cp.validate()
}

// Database holds the MariaDB connection details — stored and edited as
// separate components, assembled into a Go MySQL driver DSN by DSN() (see
// dsn.go) — plus where and how synced geo objects are written.
type Database struct {
	Host string `yaml:"host" json:"host"`
	// Port defaults to 3306 when 0.
	Port     int    `yaml:"port"     json:"port"`
	User     string `yaml:"user"     json:"user"`
	Password string `yaml:"password" json:"password"`
	// Name is the database (schema) name.
	Name string `yaml:"name" json:"name"`
	// Params are extra driver parameters in URL query form (e.g.
	// "timeout=10s"); parseTime=true is always set by DSN(). TLS is
	// configured via TLS/TLSSkipVerify, not a "tls" parameter here.
	Params string `yaml:"params" json:"params"`
	// TLS, if true, encrypts the connection to the database server.
	TLS bool `yaml:"tls" json:"tls"`
	// TLSSkipVerify, if true (and TLS is enabled), accepts any server
	// certificate instead of verifying its chain and host name — only for
	// self-signed certificates on a trusted network. Defaults to false, so
	// enabling TLS verifies the certificate unless explicitly opted out.
	TLSSkipVerify bool `yaml:"tlsSkipVerify" json:"tlsSkipVerify"`
	// TLSCACert, if set, is one or more PEM-encoded CA certificates the
	// server certificate is verified against instead of the system roots
	// (e.g. for a private CA). Only used when TLS is enabled and
	// TLSSkipVerify isn't. Not a secret, so it's never redacted.
	TLSCACert string `yaml:"tlsCaCert" json:"tlsCaCert"`
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

// WebServer configures the HTTP server that exposes sync status, recent log
// output and the config editor. It always runs.
type WebServer struct {
	// Address is the address (see net/http.Server.Addr) the server listens
	// on, e.g. ":8080" or "127.0.0.1:8080". Defaults to ":8080" if left
	// empty.
	Address string `yaml:"address" json:"address"`
}

// applyDefault fills Address with defaultWebServerAddress if Address is
// empty. Shared by Config.Validate and LoadBootstrap, since
// WebServer is now validated/defaulted in two different places (a full
// Config, and the standalone bootstrap file).
func (w *WebServer) applyDefault() {
	if w.Address == "" {
		w.Address = defaultWebServerAddress
	}
}

// Config is the root configuration document.
type Config struct {
	APIs      []API       `yaml:"apis"      json:"apis"`
	Database  Database    `yaml:"database"  json:"database"`
	Maps      []MapTarget `yaml:"maps"      json:"maps"`
	WebServer WebServer   `yaml:"webServer" json:"webServer"`
}

const defaultWebServerAddress = ":8080"

// Validate checks c for consistency, filling in defaults (Database.Table,
// Database.Columns, WebServer.Address) as it goes. Callers that assemble a
// *Config (configdb, plus the WebServer overlay applied by
// runner.Runner's apply) must call this themselves before using the result.
func (c *Config) Validate() error {
	if err := c.ValidateAPIs(); err != nil {
		return err
	}

	if err := c.Database.ValidateConnection(); err != nil {
		return err
	}

	if err := c.Database.validate(); err != nil {
		return err
	}

	c.WebServer.applyDefault()

	return c.ValidateMaps()
}

// ValidateAPIs checks every apis[] entry on its own and that no two share
// an id.
func (c *Config) ValidateAPIs() error {
	seen := make(map[string]bool, len(c.APIs))

	for i := range c.APIs {
		if err := c.APIs[i].Validate(); err != nil {
			return fmt.Errorf("apis[%d]: %w", i, err)
		}

		if seen[c.APIs[i].ID] {
			return fmt.Errorf("apis[%d].id %q is duplicated", i, c.APIs[i].ID)
		}

		seen[c.APIs[i].ID] = true
	}

	return nil
}

// API returns the configured API with the given id, or nil.
func (c *Config) API(id string) *API {
	for i := range c.APIs {
		if c.APIs[i].ID == id {
			return &c.APIs[i]
		}
	}

	return nil
}

// ValidateMaps checks every maps[] entry — including parsing its Interval
// (storing the result for SyncInterval to return), checking that its
// staticColumns are valid SQL identifiers that don't collide with a
// database.columns target, that its apiId names a configured API, and
// that no two entries share an id — split out
// from Validate to keep its cyclomatic complexity down, and exported so
// callers that validate just a candidate maps list (e.g. the runner's
// per-map create/update, which don't have api/database connection details filled in
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

		if m.APIID == "" {
			return fmt.Errorf("maps[%d].apiId is required", i)
		}

		if c.API(m.APIID) == nil {
			return fmt.Errorf("maps[%d].apiId %q is not a configured API", i, m.APIID)
		}

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
