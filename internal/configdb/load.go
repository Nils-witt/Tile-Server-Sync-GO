package configdb

import (
	"Tile-Server-Sync-GO/internal/config"
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// Load assembles a *config.Config from the stored rows. WebServer is left
// zero-valued — callers overlay the fixed bootstrap value. An empty
// database (no config_scalar row yet, e.g. a brand new install) is not an
// error: it returns a zero-valued Config, exactly as an empty YAML document
// would have under the old file-backed Load. Callers decide whether that
// state is usable by calling cfg.Validate().
func (s *Store) Load(ctx context.Context) (*config.Config, error) {
	cfg := &config.Config{}

	var pruneMissing, syncOverlays int64

	row := s.db.QueryRowContext(ctx,
		`SELECT api_base_url, api_username, api_password, api_token, db_dsn, db_table,
		        db_prune_missing, db_sync_overlays
		 FROM config_scalar WHERE id = 1`)

	switch err := row.Scan(
		&cfg.API.BaseURL, &cfg.API.Username, &cfg.API.Password, &cfg.API.Token,
		&cfg.Database.DSN, &cfg.Database.Table, &pruneMissing, &syncOverlays,
	); {
	case errors.Is(err, sql.ErrNoRows):
		// No row yet: leave cfg's scalars zero-valued.
	case err != nil:
		return nil, fmt.Errorf("load config: %w", err)
	default:
		cfg.Database.PruneMissing = pruneMissing != 0
		cfg.Database.SyncOverlays = syncOverlays != 0
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

func (s *Store) loadDatabaseColumns(ctx context.Context) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT field, column FROM database_columns`)
	if err != nil {
		return nil, fmt.Errorf("load database columns: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var columns map[string]string

	for rows.Next() {
		var field, column string
		if err := rows.Scan(&field, &column); err != nil {
			return nil, fmt.Errorf("scan database column: %w", err)
		}

		if columns == nil {
			columns = make(map[string]string)
		}

		columns[field] = column
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("load database columns: %w", err)
	}

	return columns, nil
}

// mapRow is one maps table row, before its versions/staticColumns are
// loaded.
type mapRow struct {
	rowID  int64
	target config.MapTarget
}

func (s *Store) loadMaps(ctx context.Context) ([]config.MapTarget, error) {
	mapRows, err := s.queryMapRows(ctx)
	if err != nil {
		return nil, err
	}

	if mapRows == nil {
		return nil, nil
	}

	targets := make([]config.MapTarget, len(mapRows))

	for i, mr := range mapRows {
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
		targets[i] = mr.target
	}

	return targets, nil
}

// queryMapRows reads the maps table's id/map_id columns, in configured
// order, without yet loading each map's versions/staticColumns.
func (s *Store) queryMapRows(ctx context.Context) ([]mapRow, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, map_id, name, interval FROM maps ORDER BY sort_order ASC, id ASC`)
	if err != nil {
		return nil, fmt.Errorf("load maps: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var mapRows []mapRow

	for rows.Next() {
		var mr mapRow
		if err := rows.Scan(&mr.rowID, &mr.target.ID, &mr.target.Name, &mr.target.Interval); err != nil {
			return nil, fmt.Errorf("scan map: %w", err)
		}

		mapRows = append(mapRows, mr)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("load maps: %w", err)
	}

	return mapRows, nil
}

func (s *Store) loadMapVersions(ctx context.Context, mapRowID int64) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT version FROM map_versions WHERE map_id = ? ORDER BY sort_order ASC, id ASC`, mapRowID)
	if err != nil {
		return nil, fmt.Errorf("load map versions: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var versions []string

	for rows.Next() {
		var version string
		if err := rows.Scan(&version); err != nil {
			return nil, fmt.Errorf("scan map version: %w", err)
		}

		versions = append(versions, version)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("load map versions: %w", err)
	}

	return versions, nil
}

func (s *Store) loadMapStaticColumns(ctx context.Context, mapRowID int64) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT column, value FROM map_static_columns WHERE map_id = ?`, mapRowID)
	if err != nil {
		return nil, fmt.Errorf("load map static columns: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var columns map[string]string

	for rows.Next() {
		var column, value string
		if err := rows.Scan(&column, &value); err != nil {
			return nil, fmt.Errorf("scan map static column: %w", err)
		}

		if columns == nil {
			columns = make(map[string]string)
		}

		columns[column] = value
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("load map static columns: %w", err)
	}

	return columns, nil
}
