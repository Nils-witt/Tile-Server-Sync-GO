package configdb

import (
	"context"
	"fmt"

	"github.com/Nils-witt/Tile-Server-Sync-GO/internal/config"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Save persists every field of cfg except WebServer, replacing the
// database_columns and maps (with their versions/staticColumns) tables
// wholesale inside one transaction — a delete-then-reinsert rather than a
// diff, matching the web UI's whole-form save semantics. Row ids churn on
// every save; nothing outside this package references them. Save does not
// call cfg.Validate() itself — callers validate before calling Save.
func (s *Store) Save(ctx context.Context, cfg *config.Config) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		scalar := configScalar{
			ID:              1,
			APIBaseURL:      cfg.API.BaseURL,
			APIUsername:     cfg.API.Username,
			APIPassword:     cfg.API.Password,
			APIToken:        cfg.API.Token,
			DBHost:          cfg.Database.Host,
			DBPort:          cfg.Database.Port,
			DBUser:          cfg.Database.User,
			DBPassword:      cfg.Database.Password,
			DBName:          cfg.Database.Name,
			DBParams:        cfg.Database.Params,
			DBTLS:           cfg.Database.TLS,
			DBTLSSkipVerify: cfg.Database.TLSSkipVerify,
			DBTLSCACert:     cfg.Database.TLSCACert,
			DBTable:         cfg.Database.Table,
			DBPruneMissing:  cfg.Database.PruneMissing,
			DBSyncOverlays:  cfg.Database.SyncOverlays,
		}

		err := tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "id"}},
			UpdateAll: true,
		}).Create(&scalar).Error
		if err != nil {
			return fmt.Errorf("save config: %w", err)
		}

		if err := saveDatabaseColumns(tx, cfg.Database.Columns); err != nil {
			return err
		}

		return saveMaps(tx, cfg.Maps)
	})
}

func saveDatabaseColumns(tx *gorm.DB, columns map[string]string) error {
	if err := tx.Exec("DELETE FROM database_columns").Error; err != nil {
		return fmt.Errorf("clear database columns: %w", err)
	}

	if len(columns) == 0 {
		return nil
	}

	rows := make([]databaseColumn, 0, len(columns))
	for field, column := range columns {
		rows = append(rows, databaseColumn{Field: field, Column: column})
	}

	if err := tx.Create(&rows).Error; err != nil {
		return fmt.Errorf("save database columns: %w", err)
	}

	return nil
}

func saveMaps(tx *gorm.DB, maps []config.MapTarget) error {
	if err := tx.Exec("DELETE FROM maps").Error; err != nil {
		return fmt.Errorf("clear maps: %w", err)
	}

	for i, m := range maps {
		record := mapRecordFromTarget(m, i)
		if err := tx.Create(&record).Error; err != nil {
			return fmt.Errorf("save map %q: %w", m.ID, err)
		}
	}

	return nil
}
