package configdb

import (
	"context"
	"database/sql"
	"fmt"
	"go-sync-objects/internal/config"
)

// Save persists every field of cfg except WebServer, replacing the
// database_columns and maps (with their versions/staticColumns) tables
// wholesale inside one transaction — a delete-then-reinsert rather than a
// diff, matching the web UI's whole-form save semantics. Row ids churn on
// every save; nothing outside this package references them. Save does not
// call cfg.Validate() itself — callers validate before calling Save.
func (s *Store) Save(ctx context.Context, cfg *config.Config) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	pruneMissing := 0
	if cfg.Database.PruneMissing {
		pruneMissing = 1
	}

	syncOverlays := 0
	if cfg.Database.SyncOverlays {
		syncOverlays = 1
	}

	_, err = tx.ExecContext(ctx, `
		INSERT INTO config_scalar
			(id, api_base_url, api_username, api_password, api_token, db_dsn, db_table,
			 db_prune_missing, db_sync_overlays)
		VALUES (1, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			api_base_url = excluded.api_base_url,
			api_username = excluded.api_username,
			api_password = excluded.api_password,
			api_token = excluded.api_token,
			db_dsn = excluded.db_dsn,
			db_table = excluded.db_table,
			db_prune_missing = excluded.db_prune_missing,
			db_sync_overlays = excluded.db_sync_overlays`,
		cfg.API.BaseURL, cfg.API.Username, cfg.API.Password, cfg.API.Token,
		cfg.Database.DSN, cfg.Database.Table, pruneMissing, syncOverlays)
	if err != nil {
		return fmt.Errorf("save config: %w", err)
	}

	if err := saveDatabaseColumns(ctx, tx, cfg.Database.Columns); err != nil {
		return err
	}

	if err := saveMaps(ctx, tx, cfg.Maps); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}

	return nil
}

func saveDatabaseColumns(ctx context.Context, tx *sql.Tx, columns map[string]string) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM database_columns`); err != nil {
		return fmt.Errorf("clear database columns: %w", err)
	}

	for field, column := range columns {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO database_columns (field, column) VALUES (?, ?)`, field, column); err != nil {
			return fmt.Errorf("save database column %q: %w", field, err)
		}
	}

	return nil
}

func saveMaps(ctx context.Context, tx *sql.Tx, maps []config.MapTarget) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM maps`); err != nil {
		return fmt.Errorf("clear maps: %w", err)
	}

	for i, m := range maps {
		res, err := tx.ExecContext(ctx,
			`INSERT INTO maps (map_id, name, sort_order, interval) VALUES (?, ?, ?, ?)`, m.ID, m.Name, i, m.Interval)
		if err != nil {
			return fmt.Errorf("save map %q: %w", m.ID, err)
		}

		mapRowID, err := res.LastInsertId()
		if err != nil {
			return fmt.Errorf("get row id for map %q: %w", m.ID, err)
		}

		if err := insertMapVersionsAndStaticColumns(ctx, tx, mapRowID, m); err != nil {
			return err
		}
	}

	return nil
}
