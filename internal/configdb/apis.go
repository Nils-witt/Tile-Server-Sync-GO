package configdb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/Nils-witt/Tile-Server-Sync-GO/internal/config"

	"gorm.io/gorm"
)

// Sentinel errors returned by the per-API methods below.
var (
	ErrAPIIDTaken  = errors.New("api id already taken")
	ErrAPINotFound = errors.New("api not found")
)

func (r apiRecord) toAPI() config.API {
	return config.API{
		ID: r.APIID, Name: r.Name, BaseURL: r.BaseURL, Username: r.Username, Password: r.Password, Token: r.Token,
	}
}

func apiRecordFromAPI(a config.API, sortOrder int) apiRecord {
	return apiRecord{
		APIID: a.ID, Name: a.Name, SortOrder: sortOrder,
		BaseURL: a.BaseURL, Username: a.Username, Password: a.Password, Token: a.Token,
	}
}

// loadAPIs returns every configured API, in configured order.
func (s *Store) loadAPIs(ctx context.Context) ([]config.API, error) {
	var records []apiRecord
	if err := s.db.WithContext(ctx).Order("sort_order ASC, id ASC").Find(&records).Error; err != nil {
		return nil, fmt.Errorf("load apis: %w", err)
	}

	if records == nil {
		return nil, nil
	}

	apis := make([]config.API, len(records))
	for i, r := range records {
		apis[i] = r.toAPI()
	}

	return apis, nil
}

// ListAPIs returns every configured API, in configured order.
func (s *Store) ListAPIs(ctx context.Context) ([]config.API, error) {
	return s.loadAPIs(ctx)
}

// CreateAPI inserts a new API after every currently configured one. Returns
// ErrAPIIDTaken (wrapped) if a.ID is already in use.
func (s *Store) CreateAPI(ctx context.Context, a config.API) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var maxOrder sql.NullInt64
		if err := tx.Model(&apiRecord{}).Select("MAX(sort_order)").Scan(&maxOrder).Error; err != nil {
			return fmt.Errorf("create api %q: %w", a.ID, err)
		}

		nextOrder := 0
		if maxOrder.Valid {
			nextOrder = int(maxOrder.Int64) + 1
		}

		record := apiRecordFromAPI(a, nextOrder)
		if err := tx.Create(&record).Error; err != nil {
			if errors.Is(err, gorm.ErrDuplicatedKey) {
				return fmt.Errorf("create api %q: %w", a.ID, ErrAPIIDTaken)
			}

			return fmt.Errorf("create api %q: %w", a.ID, err)
		}

		return nil
	})
}

// UpdateAPI replaces every field of the API identified by id except the id
// itself (a.ID is ignored). Returns ErrAPINotFound if id doesn't match any
// configured API.
func (s *Store) UpdateAPI(ctx context.Context, id string, a config.API) error {
	res := s.db.WithContext(ctx).Model(&apiRecord{}).Where("api_id = ?", id).
		Select("Name", "BaseURL", "Username", "Password", "Token").
		Updates(&apiRecord{Name: a.Name, BaseURL: a.BaseURL, Username: a.Username, Password: a.Password, Token: a.Token})
	if res.Error != nil {
		return fmt.Errorf("update api %q: %w", id, res.Error)
	}

	if res.RowsAffected == 0 {
		return fmt.Errorf("update api %q: %w", id, ErrAPINotFound)
	}

	return nil
}

// DeleteAPI removes the API identified by id. It doesn't check whether any
// map still refers to it; callers do. Returns ErrAPINotFound if id doesn't
// match any configured API.
func (s *Store) DeleteAPI(ctx context.Context, id string) error {
	res := s.db.WithContext(ctx).Where("api_id = ?", id).Delete(&apiRecord{})
	if res.Error != nil {
		return fmt.Errorf("delete api %q: %w", id, res.Error)
	}

	if res.RowsAffected == 0 {
		return fmt.Errorf("delete api %q: %w", id, ErrAPINotFound)
	}

	return nil
}
