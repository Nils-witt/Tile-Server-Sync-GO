package runner

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/Nils-witt/Tile-Server-Sync-GO/internal/config"
	"github.com/Nils-witt/Tile-Server-Sync-GO/internal/configdb"
	"github.com/Nils-witt/Tile-Server-Sync-GO/internal/status"
)

func newTestRunner(t *testing.T) (*Runner, *configdb.Store) {
	t.Helper()

	ctx := context.Background()

	cfgDB, err := configdb.Open(ctx, filepath.Join(t.TempDir(), "config.db"))
	if err != nil {
		t.Fatalf("open configdb: %v", err)
	}

	t.Cleanup(func() { _ = cfgDB.Close() })

	r, err := New(ctx, cfgDB, config.WebServer{Address: ":0"}, status.New())
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	return r, cfgDB
}

// TestSaveAPIKeepsBlankSecretAndPersists checks that a section save is
// persisted even though the (incomplete) config can't be applied, that a
// blank password means "unchanged", and that secrets never leave the runner.
func TestSaveAPIKeepsBlankSecretAndPersists(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	r, cfgDB := newTestRunner(t)
	actor := Actor{Username: "alice", RemoteAddr: "127.0.0.1:1"}

	if _, _, err := r.SaveAPI(ctx, actor, config.API{BaseURL: "http://a", Username: "u", Password: "secret"}); err != nil {
		t.Fatalf("SaveAPI: %v", err)
	}

	got, res, err := r.SaveAPI(ctx, actor, config.API{BaseURL: "http://b", Username: "u"})
	if err != nil {
		t.Fatalf("SaveAPI: %v", err)
	}

	if res.ApplyErr == nil {
		t.Error("ApplyErr = nil for a config without a database, want an error")
	}

	if got.API.Password != "" || r.Config().API.Password != "" {
		t.Error("password leaked out of the runner")
	}

	stored, err := cfgDB.Load(ctx)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if stored.API.BaseURL != "http://b" || stored.API.Password != "secret" {
		t.Errorf("stored API = %+v, want baseUrl http://b with the original password", stored.API)
	}

	entries, err := r.SecurityLog(ctx, 10)
	if err != nil {
		t.Fatalf("SecurityLog: %v", err)
	}

	if len(entries) != 2 || entries[0].EventType != "config_saved" || entries[0].Username != "alice" {
		t.Errorf("security log = %+v, want two config_saved entries by alice", entries)
	}
}

func TestMapLifecycle(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	r, cfgDB := newTestRunner(t)

	m := config.MapTarget{ID: "m1", Versions: []string{"current"}, Interval: "5m"}

	if _, _, err := r.CreateMap(ctx, Actor{}, m); err != nil {
		t.Fatalf("CreateMap: %v", err)
	}

	if _, _, err := r.CreateMap(ctx, Actor{}, m); !errors.Is(err, ErrInvalid) {
		t.Errorf("duplicate CreateMap err = %v, want ErrInvalid", err)
	}

	if _, _, err := r.CreateMap(ctx, Actor{}, config.MapTarget{ID: "m2"}); !errors.Is(err, ErrInvalid) {
		t.Errorf("CreateMap without versions err = %v, want ErrInvalid", err)
	}

	m.Versions = []string{"1", "2"}

	if _, _, err := r.UpdateMap(ctx, Actor{}, "m1", m); err != nil {
		t.Fatalf("UpdateMap: %v", err)
	}

	if _, _, err := r.UpdateMap(ctx, Actor{}, "nope", m); !errors.Is(err, ErrMapNotFound) {
		t.Errorf("UpdateMap unknown err = %v, want ErrMapNotFound", err)
	}

	if maps := r.Maps(); len(maps) != 1 || len(maps[0].Versions) != 2 {
		t.Errorf("Maps() = %+v, want m1 with two versions", maps)
	}

	if _, err := r.DeleteMap(ctx, Actor{}, "m1"); err != nil {
		t.Fatalf("DeleteMap: %v", err)
	}

	if _, err := r.DeleteMap(ctx, Actor{}, "m1"); !errors.Is(err, ErrMapNotFound) {
		t.Errorf("second DeleteMap err = %v, want ErrMapNotFound", err)
	}

	stored, err := cfgDB.ListMaps(ctx)
	if err != nil {
		t.Fatalf("ListMaps: %v", err)
	}

	if len(stored) != 0 || len(r.Maps()) != 0 {
		t.Errorf("maps left after delete: stored=%v in-memory=%v", stored, r.Maps())
	}
}

func TestSyncMapNotConfigured(t *testing.T) {
	t.Parallel()

	r, _ := newTestRunner(t)

	if _, err := r.SyncMap(context.Background(), "m1"); !errors.Is(err, ErrNotConfigured) {
		t.Errorf("SyncMap err = %v, want ErrNotConfigured", err)
	}
}
