package configdb

import (
	"context"
	"errors"
	"fmt"
	"log"

	"github.com/Nils-witt/Tile-Server-Sync-GO/internal/config"

	"gorm.io/gorm"
)

// Load assembles a *config.Config from the stored rows. WebServer is left
// zero-valued — callers overlay the fixed bootstrap value. An empty
// database (no config_scalar row yet, e.g. a brand new install) is not an
// error: it returns a zero-valued Config, exactly as an empty YAML document
// would have under the old file-backed Load. Callers decide whether that
// state is usable by calling cfg.Validate().
func (s *Store) Load(ctx context.Context) (*config.Config, error) {
	cfg := &config.Config{}

	var scalar configScalar

	switch err := s.db.WithContext(ctx).First(&scalar, 1).Error; {
	case errors.Is(err, gorm.ErrRecordNotFound):
		// No row yet: leave cfg's scalars zero-valued.
	case err != nil:
		return nil, fmt.Errorf("load config: %w", err)
	default:
		cfg.API.BaseURL = scalar.APIBaseURL
		cfg.API.Username = scalar.APIUsername
		cfg.API.Password = scalar.APIPassword
		cfg.API.Token = scalar.APIToken
		cfg.Database.Host = scalar.DBHost
		cfg.Database.Port = scalar.DBPort
		cfg.Database.User = scalar.DBUser
		cfg.Database.Password = scalar.DBPassword
		cfg.Database.Name = scalar.DBName
		cfg.Database.Params = scalar.DBParams
		applyLegacyDSN(&cfg.Database, scalar.DBDSN)
		cfg.Database.Table = scalar.DBTable
		cfg.Database.PruneMissing = scalar.DBPruneMissing
		cfg.Database.SyncOverlays = scalar.DBSyncOverlays
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

// applyLegacyDSN fills db's connection components from a database last
// saved before they were split out of a single DSN string. It only applies
// while no host is stored yet; the next Save writes the components and
// clears db_dsn. A DSN that can't be split (e.g. a unix socket) is logged
// and left for the user to re-enter.
func applyLegacyDSN(db *config.Database, dsn string) {
	if dsn == "" || db.Host != "" {
		return
	}

	parsed, err := config.DatabaseFromDSN(dsn)
	if err != nil {
		log.Printf("configdb: can't migrate stored database dsn, re-enter it on the config page: %v", err)
		return
	}

	db.Host, db.Port, db.User, db.Password, db.Name, db.Params =
		parsed.Host, parsed.Port, parsed.User, parsed.Password, parsed.Name, parsed.Params
}

func (s *Store) loadDatabaseColumns(ctx context.Context) (map[string]string, error) {
	var rows []databaseColumn
	if err := s.db.WithContext(ctx).Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("load database columns: %w", err)
	}

	var columns map[string]string

	for _, row := range rows {
		if columns == nil {
			columns = make(map[string]string, len(rows))
		}

		columns[row.Field] = row.Column
	}

	return columns, nil
}

// toMapTarget converts a loaded mapRecord (with its Versions/StaticColumns
// associations already preloaded) into the config.MapTarget shape callers
// outside this package deal in.
func (r mapRecord) toMapTarget() config.MapTarget {
	target := config.MapTarget{
		ID:       r.MapID,
		Name:     r.Name,
		Interval: r.Interval,
		Disabled: r.Disabled,
	}

	if len(r.Versions) > 0 {
		target.Versions = make([]string, len(r.Versions))
		for i, v := range r.Versions {
			target.Versions[i] = v.Version
		}
	}

	if len(r.StaticColumns) > 0 {
		target.StaticColumns = make(map[string]string, len(r.StaticColumns))
		for _, c := range r.StaticColumns {
			target.StaticColumns[c.Column] = c.Value
		}
	}

	return target
}

// mapRecordFromTarget builds a mapRecord (with its Versions/StaticColumns
// associations populated, unsaved) from a config.MapTarget, ready to be
// passed to *gorm.DB.Create — GORM cascades the Create to the associations'
// own tables.
func mapRecordFromTarget(m config.MapTarget, sortOrder int) mapRecord {
	record := mapRecord{
		MapID: m.ID, Name: m.Name, SortOrder: sortOrder, Interval: m.Interval, Disabled: m.Disabled,
	}

	for i, version := range m.Versions {
		record.Versions = append(record.Versions, mapVersion{Version: version, SortOrder: i})
	}

	for column, value := range m.StaticColumns {
		record.StaticColumns = append(record.StaticColumns, mapStaticColumn{Column: column, Value: value})
	}

	return record
}

// loadMaps returns every configured map, in configured order, with each
// map's Versions/StaticColumns preloaded in their own configured order.
func (s *Store) loadMaps(ctx context.Context) ([]config.MapTarget, error) {
	var records []mapRecord

	err := s.db.WithContext(ctx).
		Preload("Versions", func(db *gorm.DB) *gorm.DB { return db.Order("sort_order ASC, id ASC") }).
		Preload("StaticColumns").
		Order("sort_order ASC, id ASC").
		Find(&records).Error
	if err != nil {
		return nil, fmt.Errorf("load maps: %w", err)
	}

	if records == nil {
		return nil, nil
	}

	targets := make([]config.MapTarget, len(records))
	for i, r := range records {
		targets[i] = r.toMapTarget()
	}

	return targets, nil
}
