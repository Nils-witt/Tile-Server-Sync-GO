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

// TestUpdateAPIKeepsBlankSecretAndPersists checks that an API change is
// persisted even though the (incomplete) config can't be applied, that a
// blank password means "unchanged" (including for the pre-save test), and
// that secrets never leave the runner.
func TestUpdateAPIKeepsBlankSecretAndPersists(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	r, cfgDB := newTestRunner(t)
	actor := Actor{Username: "alice", RemoteAddr: "127.0.0.1:1"}
	urlA, urlB := newTileserve(t), newTileserve(t)

	if _, _, err := r.CreateAPI(ctx, actor, config.API{ID: "a", BaseURL: urlA, Username: "u", Password: testPassword}); err != nil {
		t.Fatalf("CreateAPI: %v", err)
	}

	got, res, err := r.UpdateAPI(ctx, actor, "a", config.API{BaseURL: urlB, Username: "u"})
	if err != nil {
		t.Fatalf("UpdateAPI: %v", err)
	}

	if res.ApplyErr == nil {
		t.Error("ApplyErr = nil for a config without a database, want an error")
	}

	if got.Password != "" || r.APIs()[0].Password != "" || r.Config().APIs[0].Password != "" {
		t.Error("password leaked out of the runner")
	}

	stored, err := cfgDB.Load(ctx)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if len(stored.APIs) != 1 || stored.APIs[0].BaseURL != urlB || stored.APIs[0].Password != testPassword {
		t.Errorf("stored APIs = %+v, want a at %s with the original password", stored.APIs, urlB)
	}

	assertEvents(ctx, t, r, "alice", "api_created", "api_updated")
}

// assertEvents checks that the security log has exactly one entry by
// username for each of eventTypes.
func assertEvents(ctx context.Context, t *testing.T, r *Runner, username string, eventTypes ...string) {
	t.Helper()

	entries, err := r.SecurityLog(ctx, 10)
	if err != nil {
		t.Fatalf("SecurityLog: %v", err)
	}

	for _, eventType := range eventTypes {
		if countEvents(entries, eventType, username) != 1 {
			t.Errorf("security log = %+v, want one %s by %s", entries, eventType, username)
		}
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

// TestSaveRejectedWhenTestFails checks that an API or database section
// failing its pre-save test (invalid settings, or a failed login/ping)
// isn't persisted.
func TestSaveRejectedWhenTestFails(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	r, cfgDB := newTestRunner(t)
	url := newTileserve(t)

	if _, _, err := r.CreateAPI(ctx, Actor{}, config.API{ID: "a", BaseURL: url}); !errors.Is(err, ErrInvalid) {
		t.Errorf("CreateAPI without credentials err = %v, want ErrInvalid", err)
	}

	if _, _, err := r.CreateAPI(ctx, Actor{}, config.API{ID: "a/b", BaseURL: url, Token: "t"}); !errors.Is(err, ErrInvalid) {
		t.Errorf("CreateAPI with invalid id err = %v, want ErrInvalid", err)
	}

	if _, _, err := r.CreateAPI(ctx, Actor{}, config.API{ID: "a", BaseURL: url, Username: "u", Password: "wrong"}); !errors.Is(err, ErrTestFailed) {
		t.Errorf("CreateAPI with wrong password err = %v, want ErrTestFailed", err)
	}

	if err := r.TestAPI(ctx, Actor{}, config.API{ID: "a", BaseURL: url, Token: "t"}); err != nil {
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

	if len(stored.APIs) != 0 || stored.Database.Host != "" {
		t.Errorf("stored config = %+v, want nothing saved", stored)
	}
}

// createTestAPI adds an API with the given id, backed by its own fake
// tileserve-go.
func createTestAPI(ctx context.Context, t *testing.T, r *Runner, id string) {
	t.Helper()

	if _, _, err := r.CreateAPI(ctx, Actor{}, config.API{ID: id, BaseURL: newTileserve(t), Token: "t"}); err != nil {
		t.Fatalf("CreateAPI %s: %v", id, err)
	}
}

func TestMapLifecycle(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	r, cfgDB := newTestRunner(t)

	m := config.MapTarget{ID: "m1", APIID: "a", Versions: []string{"current"}, Interval: "5m"}

	if _, _, err := r.CreateMap(ctx, Actor{}, m); !errors.Is(err, ErrInvalid) {
		t.Errorf("CreateMap with unknown api err = %v, want ErrInvalid", err)
	}

	createTestAPI(ctx, t, r, "a")
	createTestAPI(ctx, t, r, "b")

	if _, _, err := r.CreateMap(ctx, Actor{}, m); err != nil {
		t.Fatalf("CreateMap: %v", err)
	}

	assertCreateMapRejectsInvalid(ctx, t, r, m)

	m.Versions = []string{"1", "2"}
	m.APIID = "b"

	if _, _, err := r.UpdateMap(ctx, Actor{}, "m1", m); err != nil {
		t.Fatalf("UpdateMap: %v", err)
	}

	if _, _, err := r.UpdateMap(ctx, Actor{}, "nope", m); !errors.Is(err, ErrMapNotFound) {
		t.Errorf("UpdateMap unknown err = %v, want ErrMapNotFound", err)
	}

	if maps := r.Maps(); len(maps) != 1 || len(maps[0].Versions) != 2 || maps[0].APIID != "b" {
		t.Errorf("Maps() = %+v, want m1 on api b with two versions", maps)
	}

	assertDeleteAPIOnlyWhenUnused(ctx, t, r)

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

// assertCreateMapRejectsInvalid expects existing to be configured already.
func assertCreateMapRejectsInvalid(ctx context.Context, t *testing.T, r *Runner, existing config.MapTarget) {
	t.Helper()

	if _, _, err := r.CreateMap(ctx, Actor{}, existing); !errors.Is(err, ErrInvalid) {
		t.Errorf("duplicate CreateMap err = %v, want ErrInvalid", err)
	}

	if _, _, err := r.CreateMap(ctx, Actor{}, config.MapTarget{ID: "m2", APIID: "a"}); !errors.Is(err, ErrInvalid) {
		t.Errorf("CreateMap without versions err = %v, want ErrInvalid", err)
	}
}

// assertDeleteAPIOnlyWhenUnused expects api "b" to be used by a map and
// api "a" not to be.
func assertDeleteAPIOnlyWhenUnused(ctx context.Context, t *testing.T, r *Runner) {
	t.Helper()

	if _, err := r.DeleteAPI(ctx, Actor{}, "b"); !errors.Is(err, ErrAPIInUse) {
		t.Errorf("DeleteAPI of a used api err = %v, want ErrAPIInUse", err)
	}

	if _, err := r.DeleteAPI(ctx, Actor{}, "a"); err != nil {
		t.Errorf("DeleteAPI of an unused api: %v", err)
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

	if _, err := r.RemoteMaps(ctx, "a"); !errors.Is(err, ErrAPINotFound) {
		t.Errorf("RemoteMaps for an unknown api err = %v, want ErrAPINotFound", err)
	}

	if _, _, err := r.CreateAPI(ctx, Actor{}, config.API{ID: "a", BaseURL: newTileserve(t), Username: "u", Password: testPassword}); err != nil {
		t.Fatalf("CreateAPI: %v", err)
	}

	remote, err := r.RemoteMaps(ctx, "a")
	if err != nil {
		t.Fatalf("RemoteMaps: %v", err)
	}

	if len(remote) != 1 || remote[0].UUID != "m1" || remote[0].Name != "Town" {
		t.Errorf("RemoteMaps() = %+v, want the one fake map", remote)
	}
}
