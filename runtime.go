package main

import (
	"context"
	"errors"
	"fmt"
	"go-sync-objects/internal/config"
	"go-sync-objects/internal/configdb"
	"go-sync-objects/internal/status"
	"go-sync-objects/internal/store"
	"go-sync-objects/internal/tileserve"
	"log"
	"sync"
)

// runtime holds the reloadable pieces of a sync run — the parsed config, the
// tileserve client built from it, and the database connection built from
// it — behind a mutex, so saving a config change through the config web UI
// can swap in a freshly loaded config without restarting the process.
// current() is called
// once per sync (by runSync, right before calling syncAll) rather than held
// for a run's whole lifetime, so a reload between syncs takes effect on the
// very next one. cfg/client/db start nil and stay nil until the first
// successful reload — see configured() — since the SQLite-backed config may
// start out empty on a fresh install. cfgDB and webServer are fixed for the
// process's lifetime: webServer settings are deliberately not reloadable
// (the server a reload request arrives on can't safely restart itself mid
// request, so applying a changed webServer.enabled/address still requires a
// process restart), and cfgDB's path is bootstrap-fixed too.
type runtime struct {
	mu     sync.RWMutex
	cfg    *config.Config
	client *tileserve.Client
	db     *store.Store

	// syncMu serializes calls to syncAll made through runSync, so a manual
	// "sync now" request from the web UI can't run concurrently with a
	// scheduled runLoop tick (or another manual request): two overlapping
	// syncs of the same map/version could race on pruneMissing deleting rows
	// the other's insert just wrote.
	syncMu sync.Mutex

	cfgDB     *configdb.Store
	webServer config.WebServer

	// wake is pinged by reload() after a successful config swap, so runLoop
	// (which otherwise sleeps for up to nextWake's computed duration) can
	// react immediately — e.g. syncing a newly added map right away instead
	// of waiting out whatever sleep duration was already in flight. Buffered
	// size 1 and sent to non-blockingly: at most one pending wake needs to be
	// coalesced, since runLoop just re-reads rt.current() from scratch on
	// every tick regardless of why it woke up.
	wake chan struct{}
}

func newRuntime(cfgDB *configdb.Store, webServer config.WebServer) *runtime {
	return &runtime{cfgDB: cfgDB, webServer: webServer, wake: make(chan struct{}, 1)}
}

// current returns the runtime's current config, client, and database. Any
// of the three may be nil if the runtime has never had a successful reload.
func (rt *runtime) current() (*config.Config, *tileserve.Client, *store.Store) {
	rt.mu.RLock()
	defer rt.mu.RUnlock()

	return rt.cfg, rt.client, rt.db
}

// configured reports whether the runtime has a working {cfg, client, db}
// triple yet. False only until the first successful reload() call against a
// valid config — normal, not an error, right after a fresh install with an
// empty SQLite-backed config.
func (rt *runtime) configured() bool {
	rt.mu.RLock()
	defer rt.mu.RUnlock()

	return rt.db != nil
}

// errNotConfigured is returned by runSync/runSyncMaps when no successful
// reload has happened yet.
var errNotConfigured = errors.New("not configured yet: use /config to enter and save configuration")

// runSync runs syncAll once against every map in the runtime's current
// config, serialized against any other call to runSync/runSyncMaps via
// syncMu. It is what both the run-once path (no interval configured on any
// map) and the web UI's manual "sync now" request go through.
func (rt *runtime) runSync(ctx context.Context, rec *status.Recorder) (int, error) {
	rt.syncMu.Lock()
	defer rt.syncMu.Unlock()

	cfg, client, db := rt.current()
	if db == nil {
		rec.RecordRun(0, errNotConfigured)
		return 0, errNotConfigured
	}

	return syncAll(ctx, cfg.Maps, client, db, rec)
}

// runSyncMaps runs syncAll against just the maps in the runtime's current
// config whose ID is in ids, serialized the same way runSync is. It's what
// runLoop's per-map interval scheduler calls with the set of currently-due
// map IDs, re-reading rt.current() itself (rather than trusting maps handed
// to it earlier) so it always syncs each map's latest configured versions/
// staticColumns, even if a reload landed between runLoop computing ids and
// this call acquiring syncMu.
func (rt *runtime) runSyncMaps(ctx context.Context, rec *status.Recorder, ids map[string]struct{}) (int, error) {
	rt.syncMu.Lock()
	defer rt.syncMu.Unlock()

	cfg, client, db := rt.current()
	if db == nil {
		rec.RecordRun(0, errNotConfigured)
		return 0, errNotConfigured
	}

	due := make([]config.MapTarget, 0, len(ids))

	for _, m := range cfg.Maps {
		if _, ok := ids[m.ID]; ok {
			due = append(due, m)
		}
	}

	if len(due) == 0 {
		return 0, nil
	}

	return syncAll(ctx, due, client, db, rec)
}

// reload loads the current configdb-backed config, overlays the fixed
// bootstrap WebServer, validates it, and — only if that succeeds — builds a
// fresh client (logging in again unless a token is configured) and database
// connection, then swaps them in. On any failure — an invalid config, a
// login failure, a database connection failure — the previous state (which
// may be the initial nil state) is left in place and the error is returned,
// so a bad edit doesn't take down an otherwise-running sync.
//
// The previous database connection is closed only after the swap, once it's
// no longer reachable via current() (nil on the very first successful
// reload from an empty state). A sync already in flight at the moment of
// the swap keeps using the connection it already fetched and may see it
// close out from under it; that sync just fails and logs an error,
// self-correcting on the next iteration with the new state. That's an
// acceptable tradeoff for a manually triggered, infrequent action, rather
// than adding reference counting around every database use.
func (rt *runtime) reload(ctx context.Context) error {
	cfg, err := rt.cfgDB.Load(ctx)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	cfg.WebServer = rt.webServer

	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("invalid config: %w", err)
	}

	client, err := newClient(ctx, cfg)
	if err != nil {
		return fmt.Errorf("login: %w", err)
	}

	db, err := openStore(ctx, cfg)
	if err != nil {
		return err
	}

	rt.mu.Lock()
	oldDB := rt.db
	rt.cfg, rt.client, rt.db = cfg, client, db
	rt.mu.Unlock()

	if oldDB != nil {
		if closeErr := oldDB.Close(); closeErr != nil {
			log.Printf("close previous database connection: %v", closeErr)
		}
	}

	log.Print("config reloaded")

	select {
	case rt.wake <- struct{}{}:
	default:
	}

	return nil
}

// newClient builds a tileserve client for cfg.API, logging in unless a
// token is already configured.
func newClient(ctx context.Context, cfg *config.Config) (*tileserve.Client, error) {
	client := tileserve.New(cfg.API.BaseURL)
	if cfg.API.Token != "" {
		client.SetToken(cfg.API.Token)
		return client, nil
	}

	log.Printf("logging in to %s as %s", cfg.API.BaseURL, cfg.API.Username)

	if err := client.Login(ctx, cfg.API.Username, cfg.API.Password); err != nil {
		return nil, err
	}

	return client, nil
}

// openStore opens and prepares the database connection for cfg.Database.
func openStore(ctx context.Context, cfg *config.Config) (*store.Store, error) {
	db, err := store.Open(ctx, cfg.Database, cfg.StaticColumnNames())
	if err != nil {
		return nil, fmt.Errorf("connect to database: %w", err)
	}

	if err := db.EnsureSchema(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}

	return db, nil
}
