package configdb

import (
	"Tile-Server-Sync-GO/internal/config"
	"context"
	"errors"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

// tableColumns returns a table's column names via PRAGMA table_info, used
// to verify the embedded-Permissions naming-strategy assumptions documented
// on User/SSOConfig in users.go/sso.go.
func tableColumns(t *testing.T, s *Store, table string) []string {
	t.Helper()

	rows, err := s.db.Raw("PRAGMA table_info(" + table + ")").Rows()
	if err != nil {
		t.Fatalf("pragma table_info(%s): %v", table, err)
	}
	defer func() {
		if err := rows.Close(); err != nil {
			t.Fatalf("close rows: %v", err)
		}
	}()

	var cols []string

	for rows.Next() {
		var (
			cid         int
			name, ctype string
			notnull, pk int
			dflt        any
		)
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			t.Fatalf("scan: %v", err)
		}

		cols = append(cols, name)
	}

	if err := rows.Err(); err != nil {
		t.Fatalf("iterate rows: %v", err)
	}

	return cols
}

func assertContainsAll(t *testing.T, table string, got, want []string) {
	t.Helper()

	for _, w := range want {
		if !slices.Contains(got, w) {
			t.Errorf("missing expected column %q in %s, got %v", w, table, got)
		}
	}
}

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

func TestEmbeddedPermissionColumnNames(t *testing.T) {
	t.Parallel()

	s, _ := openTestStore(t)

	assertContainsAll(t, "users", tableColumns(t, s, "users"), []string{
		"perm_view_status", "perm_trigger_sync", "perm_view_config",
		"perm_edit_config_api", "perm_edit_config_database", "perm_edit_config_maps", "perm_edit_config_sso",
	})

	assertContainsAll(t, "sso_config", tableColumns(t, s, "sso_config"), []string{
		"default_view_status", "default_trigger_sync", "default_view_config",
		"default_edit_config_api", "default_edit_config_database", "default_edit_config_maps", "default_edit_config_sso",
	})
}

func TestUserSessionRoundtrip(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s, _ := openTestStore(t)

	u, err := s.CreateUser(ctx, "alice", "hunter2", Permissions{ViewStatus: true, EditConfigSSO: true}, true)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	if !u.Permissions.ViewStatus || !u.Permissions.EditConfigSSO || !u.IsSuperuser {
		t.Errorf("permissions not roundtripped: %+v", u)
	}

	got, err := s.VerifyPassword(ctx, "alice", "hunter2")
	if err != nil {
		t.Fatalf("verify password: %v", err)
	}

	if got.ID != u.ID {
		t.Errorf("id mismatch")
	}

	if _, err := s.VerifyPassword(ctx, "alice", "wrong"); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("expected ErrInvalidCredentials, got %v", err)
	}

	token, exp, err := s.CreateSession(ctx, u.ID, time.Hour)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	if token == "" || exp.Before(time.Now()) {
		t.Errorf("bad session")
	}

	su, err := s.SessionUser(ctx, token)
	if err != nil {
		t.Fatalf("session user: %v", err)
	}

	if su.ID != u.ID {
		t.Errorf("session user mismatch")
	}
}

func TestDeleteUserCascadesSessions(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s, _ := openTestStore(t)

	bob, err := s.CreateUser(ctx, "bob", "hunter2", Permissions{}, false)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	token, _, err := s.CreateSession(ctx, bob.ID, time.Hour)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	if err := s.DeleteUser(ctx, bob.ID); err != nil {
		t.Fatalf("delete user: %v", err)
	}

	if _, err := s.SessionUser(ctx, token); !errors.Is(err, ErrSessionInvalid) {
		t.Errorf("sessions did not cascade-delete on fresh install: %v", err)
	}
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

func TestSSOConfigRoundtrip(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s, _ := openTestStore(t)

	cfg := &SSOConfig{Enabled: true, IssuerURL: "https://issuer", DefaultPermissions: Permissions{ViewStatus: true}}
	if err := s.SaveSSOConfig(ctx, cfg); err != nil {
		t.Fatalf("save sso: %v", err)
	}

	loaded, err := s.LoadSSOConfig(ctx)
	if err != nil {
		t.Fatalf("load sso: %v", err)
	}

	if !loaded.Enabled || loaded.IssuerURL != "https://issuer" || !loaded.DefaultPermissions.ViewStatus {
		t.Errorf("sso mismatch: %+v", loaded)
	}
}
