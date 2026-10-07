package runner

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/Nils-witt/Tile-Server-Sync-GO/internal/config"
	"github.com/Nils-witt/Tile-Server-Sync-GO/internal/configdb"
	"github.com/Nils-witt/Tile-Server-Sync-GO/internal/status"
)

// testPassword is the only password the fake tileserve-go accepts.
const testPassword = "secret"

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

// newTileserve starts a fake tileserve-go whose POST /login accepts only
// password "secret" (issuing token "t"), and whose GET /maps lists one map
// for that token.
func newTileserve(t *testing.T) string {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path == "/maps" && req.Header.Get("Authorization") == "Bearer t" {
			_, _ = w.Write([]byte(`[{"uuid":"m1","name":"Town","currentVersion":"3"}]`))
			return
		}

		var body struct{ Password string }
		if req.URL.Path != "/login" || json.NewDecoder(req.Body).Decode(&body) != nil || body.Password != testPassword {
			http.Error(w, "nope", http.StatusUnauthorized)
			return
		}

		_, _ = w.Write([]byte(`{"token":"t"}`))
	}))
	t.Cleanup(srv.Close)

	return srv.URL
}

// TestSaveAPIKeepsBlankSecretAndPersists checks that a section save is
// persisted even though the (incomplete) config can't be applied, that a
// blank password means "unchanged" (including for the pre-save test), and
// that secrets never leave the runner.
func TestSaveAPIKeepsBlankSecretAndPersists(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	r, cfgDB := newTestRunner(t)
	actor := Actor{Username: "alice", RemoteAddr: "127.0.0.1:1"}
	urlA, urlB := newTileserve(t), newTileserve(t)

	if _, _, err := r.SaveAPI(ctx, actor, config.API{BaseURL: urlA, Username: "u", Password: testPassword}); err != nil {
		t.Fatalf("SaveAPI: %v", err)
	}

	got, res, err := r.SaveAPI(ctx, actor, config.API{BaseURL: urlB, Username: "u"})
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

	if stored.API.BaseURL != urlB || stored.API.Password != testPassword {
		t.Errorf("stored API = %+v, want baseUrl %s with the original password", stored.API, urlB)
	}

	entries, err := r.SecurityLog(ctx, 10)
	if err != nil {
		t.Fatalf("SecurityLog: %v", err)
	}

	if countEvents(entries, "config_saved", "alice") != 2 {
		t.Errorf("security log = %+v, want two config_saved entries by alice", entries)
	}
}

func countEvents(entries []SecurityLogEntry, eventType, username string) int {
	n := 0

	for _, e := range entries {
		if e.EventType == eventType && e.Username == username {
			n++
		}
	}

	return n
}

// TestSaveSectionRejectedWhenTestFails checks that a section failing its
// pre-save test (invalid settings, or a failed login/ping) isn't persisted.
func TestSaveSectionRejectedWhenTestFails(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	r, cfgDB := newTestRunner(t)
	url := newTileserve(t)

	if _, _, err := r.SaveAPI(ctx, Actor{}, config.API{BaseURL: url}); !errors.Is(err, ErrInvalid) {
		t.Errorf("SaveAPI without credentials err = %v, want ErrInvalid", err)
	}

	if _, _, err := r.SaveAPI(ctx, Actor{}, config.API{BaseURL: url, Username: "u", Password: "wrong"}); !errors.Is(err, ErrTestFailed) {
		t.Errorf("SaveAPI with wrong password err = %v, want ErrTestFailed", err)
	}

	if err := r.TestAPI(ctx, Actor{}, config.API{BaseURL: url, Token: "t"}); err != nil {
		t.Errorf("TestAPI with token: %v", err)
	}

	db := config.Database{Host: "127.0.0.1", Port: 1, User: "u", Name: "n", Table: "not valid"}
	if _, _, err := r.SaveDatabase(ctx, Actor{}, db); !errors.Is(err, ErrInvalid) {
		t.Errorf("SaveDatabase with invalid table err = %v, want ErrInvalid", err)
	}

	db.Table = ""
	if _, _, err := r.SaveDatabase(ctx, Actor{}, db); !errors.Is(err, ErrTestFailed) {
		t.Errorf("SaveDatabase with unreachable host err = %v, want ErrTestFailed", err)
	}

	stored, err := cfgDB.Load(ctx)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if stored.API.BaseURL != "" || stored.Database.Host != "" {
		t.Errorf("stored config = %+v, want nothing saved", stored)
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

func TestRemoteMaps(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	r, _ := newTestRunner(t)

	if _, err := r.RemoteMaps(ctx); !errors.Is(err, ErrInvalid) {
		t.Errorf("RemoteMaps without API err = %v, want ErrInvalid", err)
	}

	if _, _, err := r.SaveAPI(ctx, Actor{}, config.API{BaseURL: newTileserve(t), Username: "u", Password: testPassword}); err != nil {
		t.Fatalf("SaveAPI: %v", err)
	}

	remote, err := r.RemoteMaps(ctx)
	if err != nil {
		t.Fatalf("RemoteMaps: %v", err)
	}

	if len(remote) != 1 || remote[0].UUID != "m1" || remote[0].Name != "Town" {
		t.Errorf("RemoteMaps() = %+v, want the one fake map", remote)
	}
}
