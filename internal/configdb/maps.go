package configdb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"go-sync-objects/internal/config"
)

// Sentinel errors returned by the per-map methods below, following
// ErrUsernameTaken/ErrUserNotFound's shape.
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
	var mr mapRow

	row := s.db.QueryRowContext(ctx, `SELECT id, map_id, interval FROM maps WHERE map_id = ?`, id)

	switch err := row.Scan(&mr.rowID, &mr.target.ID, &mr.target.Interval); {
	case errors.Is(err, sql.ErrNoRows):
		return nil, fmt.Errorf("get map %q: %w", id, ErrMapNotFound)
	case err != nil:
		return nil, fmt.Errorf("get map %q: %w", id, err)
	}

	versions, err := s.loadMapVersions(ctx, mr.rowID)
	if err != nil {
		return nil, err
	}

	staticColumns, err := s.loadMapStaticColumns(ctx, mr.rowID)
	if err != nil {
		return nil, err
	}

	mr.target.Versions = versions
	mr.target.StaticColumns = staticColumns

	return &mr.target, nil
}

// CreateMap inserts a new map, appended after every currently configured map
// (matching the order a whole-list save used to produce). Returns
// ErrMapIDTaken (wrapped) if m.ID is already in use.
func (s *Store) CreateMap(ctx context.Context, m config.MapTarget) (*config.MapTarget, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var nextOrder int
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(sort_order), -1) + 1 FROM maps`).Scan(&nextOrder); err != nil {
		return nil, fmt.Errorf("create map %q: %w", m.ID, err)
	}

	res, err := tx.ExecContext(ctx,
		`INSERT INTO maps (map_id, sort_order, interval) VALUES (?, ?, ?)`, m.ID, nextOrder, m.Interval)
	if err != nil {
		if isUniqueConstraintErr(err) {
			return nil, fmt.Errorf("create map %q: %w", m.ID, ErrMapIDTaken)
		}

		return nil, fmt.Errorf("create map %q: %w", m.ID, err)
	}

	mapRowID, err := res.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("create map %q: %w", m.ID, err)
	}

	if err := insertMapVersionsAndStaticColumns(ctx, tx, mapRowID, m); err != nil {
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit transaction: %w", err)
	}

	created := m

	return &created, nil
}

// UpdateMap replaces the versions/interval/staticColumns of the map
// identified by id — a delete-then-reinsert of its versions/staticColumns
// rows, matching saveMaps' whole-table style but scoped to one map. id (the
// URL's resource key) is authoritative: m.ID is ignored, so this can never
// rename a map. Returns ErrMapNotFound if id doesn't match any configured
// map.
func (s *Store) UpdateMap(ctx context.Context, id string, m config.MapTarget) (*config.MapTarget, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var mapRowID int64

	switch err := tx.QueryRowContext(ctx, `SELECT id FROM maps WHERE map_id = ?`, id).Scan(&mapRowID); {
	case errors.Is(err, sql.ErrNoRows):
		return nil, fmt.Errorf("update map %q: %w", id, ErrMapNotFound)
	case err != nil:
		return nil, fmt.Errorf("update map %q: %w", id, err)
	}

	if _, err := tx.ExecContext(ctx, `UPDATE maps SET interval = ? WHERE id = ?`, m.Interval, mapRowID); err != nil {
		return nil, fmt.Errorf("update map %q: %w", id, err)
	}

	if _, err := tx.ExecContext(ctx, `DELETE FROM map_versions WHERE map_id = ?`, mapRowID); err != nil {
		return nil, fmt.Errorf("update map %q: %w", id, err)
	}

	if _, err := tx.ExecContext(ctx, `DELETE FROM map_static_columns WHERE map_id = ?`, mapRowID); err != nil {
		return nil, fmt.Errorf("update map %q: %w", id, err)
	}

	updated := m
	updated.ID = id

	if err := insertMapVersionsAndStaticColumns(ctx, tx, mapRowID, updated); err != nil {
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit transaction: %w", err)
	}

	return &updated, nil
}

// DeleteMap removes the map identified by id (cascading to its versions/
// staticColumns rows via the maps table's ON DELETE CASCADE references).
// Returns ErrMapNotFound if id doesn't match any configured map.
func (s *Store) DeleteMap(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM maps WHERE map_id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete map %q: %w", id, err)
	}

	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("delete map %q: %w", id, err)
	}

	if n == 0 {
		return fmt.Errorf("delete map %q: %w", id, ErrMapNotFound)
	}

	return nil
}

// insertMapVersionsAndStaticColumns inserts m's versions and staticColumns
// rows for the maps-table row mapRowID, shared by saveMaps (whole-list
// delete-then-reinsert, in configdb.go) and CreateMap/UpdateMap above.
func insertMapVersionsAndStaticColumns(ctx context.Context, tx *sql.Tx, mapRowID int64, m config.MapTarget) error {
	for j, version := range m.Versions {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO map_versions (map_id, version, sort_order) VALUES (?, ?, ?)`,
			mapRowID, version, j); err != nil {
			return fmt.Errorf("save version %q for map %q: %w", version, m.ID, err)
		}
	}

	for column, value := range m.StaticColumns {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO map_static_columns (map_id, column, value) VALUES (?, ?, ?)`,
			mapRowID, column, value); err != nil {
			return fmt.Errorf("save static column %q for map %q: %w", column, m.ID, err)
		}
	}

	return nil
}

// ensureMapsUniqueIndex adds a UNIQUE index on maps.map_id if it doesn't
// already exist, so map_id can serve as the stable per-map resource
// identifier the GetMap/UpdateMap/DeleteMap methods above key on. Nothing
// enforced map_id uniqueness before it became an API identity; on an
// existing install where two rows already share one (the whole-list save
// this replaced never checked), this fails outright at startup rather than
// leaving {id}-addressed lookups ambiguous.
func (s *Store) ensureMapsUniqueIndex(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `CREATE UNIQUE INDEX IF NOT EXISTS idx_maps_map_id ON maps(map_id)`); err != nil {
		return fmt.Errorf(
			"create unique index on maps.map_id (likely duplicate map ids exist — resolve them first): %w", err,
		)
	}

	return nil
}
