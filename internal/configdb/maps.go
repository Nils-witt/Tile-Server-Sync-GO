package configdb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/Nils-witt/Tile-Server-Sync-GO/internal/config"

	"gorm.io/gorm"
)

// Sentinel errors returned by the per-map methods below.
var (
	ErrMapIDTaken  = errors.New("map id already taken")
	ErrMapNotFound = errors.New("map not found")
)

// ListMaps returns every configured map, in configured order — an exported
// wrapper around the same loadMaps used internally by Load.
func (s *Store) ListMaps(ctx context.Context) ([]config.MapTarget, error) {
	return s.loadMaps(ctx)
}

// GetMap returns the single map identified by id (config.MapTarget.ID, not
// the internal autoincrement row id), or ErrMapNotFound if none matches.
func (s *Store) GetMap(ctx context.Context, id string) (*config.MapTarget, error) {
	var record mapRecord

	err := s.db.WithContext(ctx).
		Preload("Versions", func(db *gorm.DB) *gorm.DB { return db.Order("sort_order ASC, id ASC") }).
		Preload("StaticColumns").
		Where("map_id = ?", id).
		First(&record).Error

	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		return nil, fmt.Errorf("get map %q: %w", id, ErrMapNotFound)
	case err != nil:
		return nil, fmt.Errorf("get map %q: %w", id, err)
	}

	target := record.toMapTarget()

	return &target, nil
}

// CreateMap inserts a new map, appended after every currently configured map
// (matching the order a whole-list save used to produce). Returns
// ErrMapIDTaken (wrapped) if m.ID is already in use.
func (s *Store) CreateMap(ctx context.Context, m config.MapTarget) (*config.MapTarget, error) {
	var created config.MapTarget

	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var maxOrder sql.NullInt64
		if err := tx.Model(&mapRecord{}).Select("MAX(sort_order)").Scan(&maxOrder).Error; err != nil {
			return fmt.Errorf("create map %q: %w", m.ID, err)
		}

		nextOrder := 0
		if maxOrder.Valid {
			nextOrder = int(maxOrder.Int64) + 1
		}

		record := mapRecordFromTarget(m, nextOrder)

		if err := tx.Create(&record).Error; err != nil {
			if errors.Is(err, gorm.ErrDuplicatedKey) {
				return fmt.Errorf("create map %q: %w", m.ID, ErrMapIDTaken)
			}

			return fmt.Errorf("create map %q: %w", m.ID, err)
		}

		created = record.toMapTarget()

		return nil
	})
	if err != nil {
		return nil, err
	}

	return &created, nil
}

// UpdateMap replaces the versions/interval/staticColumns of the map
// identified by id — a delete-then-reinsert of its versions/staticColumns
// rows, matching saveMaps' whole-table style but scoped to one map. id (the
// URL's resource key) is authoritative: m.ID is ignored, so this can never
// rename a map. Returns ErrMapNotFound if id doesn't match any configured
// map.
func (s *Store) UpdateMap(ctx context.Context, id string, m config.MapTarget) (*config.MapTarget, error) {
	var updated config.MapTarget

	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var record mapRecord

		switch err := tx.Where("map_id = ?", id).First(&record).Error; {
		case errors.Is(err, gorm.ErrRecordNotFound):
			return fmt.Errorf("update map %q: %w", id, ErrMapNotFound)
		case err != nil:
			return fmt.Errorf("update map %q: %w", id, err)
		}

		record.Name = m.Name
		record.Interval = m.Interval
		record.Disabled = m.Disabled

		if err := tx.Model(&record).Select("Name", "Interval", "Disabled").Updates(&record).Error; err != nil {
			return fmt.Errorf("update map %q: %w", id, err)
		}

		if err := tx.Where("map_id = ?", record.ID).Delete(&mapVersion{}).Error; err != nil {
			return fmt.Errorf("update map %q: %w", id, err)
		}

		if err := tx.Where("map_id = ?", record.ID).Delete(&mapStaticColumn{}).Error; err != nil {
			return fmt.Errorf("update map %q: %w", id, err)
		}

		for i, version := range m.Versions {
			v := mapVersion{MapRowID: record.ID, Version: version, SortOrder: i}
			if err := tx.Create(&v).Error; err != nil {
				return fmt.Errorf("save version %q for map %q: %w", version, id, err)
			}
		}

		for column, value := range m.StaticColumns {
			c := mapStaticColumn{MapRowID: record.ID, Column: column, Value: value}
			if err := tx.Create(&c).Error; err != nil {
				return fmt.Errorf("save static column %q for map %q: %w", column, id, err)
			}
		}

		updated = m
		updated.ID = id

		return nil
	})
	if err != nil {
		return nil, err
	}

	return &updated, nil
}

// DeleteMap removes the map identified by id (cascading to its versions/
// staticColumns rows via the maps table's ON DELETE CASCADE references).
// Returns ErrMapNotFound if id doesn't match any configured map.
func (s *Store) DeleteMap(ctx context.Context, id string) error {
	res := s.db.WithContext(ctx).Where("map_id = ?", id).Delete(&mapRecord{})
	if res.Error != nil {
		return fmt.Errorf("delete map %q: %w", id, res.Error)
	}

	if res.RowsAffected == 0 {
		return fmt.Errorf("delete map %q: %w", id, ErrMapNotFound)
	}

	return nil
}
