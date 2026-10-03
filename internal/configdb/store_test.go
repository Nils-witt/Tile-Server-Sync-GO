package configdb

import (
	"Tile-Server-Sync-GO/internal/config"
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func openTestStore(t *testing.T) (*Store, string) {
	t.Helper()

	dbPath := filepath.Join(t.TempDir(), "config.db")

	s, err := Open(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Fatalf("close: %v", err)
		}
	})

	return s, dbPath
}

func TestMapCRUD(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s, _ := openTestStore(t)

	m := config.MapTarget{
		ID: "map-1", Name: "Map One", Versions: []string{"1", "2"},
		StaticColumns: map[string]string{"region": "eu"}, Interval: "5m",
	}

	created, err := s.CreateMap(ctx, m)
	if err != nil {
		t.Fatalf("create map: %v", err)
	}

	if created.ID != "map-1" || len(created.Versions) != 2 || created.StaticColumns["region"] != "eu" {
		t.Errorf("map not roundtripped: %+v", created)
	}

	assertMapUpdateAndList(ctx, t, s)
	assertMapDeleteCascadesAndReusesID(ctx, t, s, m)
}

func assertMapUpdateAndList(ctx context.Context, t *testing.T, s *Store) {
	t.Helper()

	got, err := s.GetMap(ctx, "map-1")
	if err != nil {
		t.Fatalf("get map: %v", err)
	}

	if got.Name != "Map One" || got.Interval != "5m" {
		t.Errorf("get map mismatch: %+v", got)
	}

	updated, err := s.UpdateMap(ctx, "map-1", config.MapTarget{Name: "Renamed", Versions: []string{"3"}, Disabled: true})
	if err != nil {
		t.Fatalf("update map: %v", err)
	}

	if updated.Name != "Renamed" || !updated.Disabled || len(updated.Versions) != 1 || updated.Versions[0] != "3" {
		t.Errorf("update map mismatch: %+v", updated)
	}

	list, err := s.ListMaps(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("list maps: %v %v", list, err)
	}
}

func assertMapDeleteCascadesAndReusesID(ctx context.Context, t *testing.T, s *Store, m config.MapTarget) {
	t.Helper()

	if err := s.DeleteMap(ctx, m.ID); err != nil {
		t.Fatalf("delete map: %v", err)
	}

	var orphanVersions int64
	if err := s.db.WithContext(ctx).Model(&mapVersion{}).Count(&orphanVersions).Error; err != nil {
		t.Fatalf("count versions: %v", err)
	}

	if orphanVersions != 0 {
		t.Errorf("map_versions did not cascade-delete on fresh install: %d rows left", orphanVersions)
	}

	if _, err := s.CreateMap(ctx, m); err != nil {
		t.Fatalf("recreate map after delete: %v", err)
	}

	if _, err := s.CreateMap(ctx, m); !errors.Is(err, ErrMapIDTaken) {
		t.Errorf("expected ErrMapIDTaken, got %v", err)
	}
}

func TestSaveLoadConfigRoundtrip(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s, dbPath := openTestStore(t)

	cfg := &config.Config{}
	cfg.API.BaseURL = "https://example.com"
	cfg.API.Token = "tok"
	cfg.Database.DSN = "user:pass@/db"
	cfg.Database.Table = "geo_objects"
	cfg.Database.PruneMissing = true
	cfg.Database.Columns = map[string]string{"uuid": "uuid"}
	cfg.Maps = []config.MapTarget{{ID: "m2", Versions: []string{"current"}}}

	if err := s.Save(ctx, cfg); err != nil {
		t.Fatalf("save: %v", err)
	}

	loaded, err := s.Load(ctx)
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	if loaded.API.BaseURL != "https://example.com" || !loaded.Database.PruneMissing ||
		len(loaded.Maps) != 1 || loaded.Maps[0].ID != "m2" {
		t.Errorf("load mismatch: %+v", loaded)
	}

	// Reopening the same DB file must be idempotent (safe to AutoMigrate on
	// every startup) and must not lose data.
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	s2, err := Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer func() {
		if err := s2.Close(); err != nil {
			t.Fatalf("close reopened store: %v", err)
		}
	}()

	reloaded, err := s2.Load(ctx)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}

	if reloaded.API.BaseURL != "https://example.com" {
		t.Errorf("data lost across reopen: %+v", reloaded)
	}
}

func TestSecurityLogRoundtrip(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s, _ := openTestStore(t)

	if err := s.LogSecurityEvent(ctx, "login_success", "alice", "127.0.0.1", "detail"); err != nil {
		t.Fatalf("log: %v", err)
	}

	entries, err := s.ListSecurityLog(ctx, 10)
	if err != nil || len(entries) != 1 {
		t.Fatalf("list log: %v %v", entries, err)
	}

	if entries[0].At.IsZero() {
		t.Errorf("timestamp not parsed back: %+v", entries[0])
	}
}
