package main

import (
	"context"
	"fmt"
	"go-sync-objects/internal/config"
	"go-sync-objects/internal/status"
	"go-sync-objects/internal/store"
	"go-sync-objects/internal/tileserve"
	"log"
	"sync"
)

// runtime holds the reloadable pieces of a sync run — the parsed config, the
// tileserve client built from it, and the database connection built from
// it — behind a mutex, so the config web UI's reload button can swap in a
// freshly loaded config without restarting the process. current() is called
// once per sync (by runSync, right before calling syncAll) rather than held
// for a run's whole lifetime, so a reload between syncs takes effect on the
// very next one. webServer settings are deliberately not part of this: the
// server a reload request arrives on can't safely restart itself mid
// request, so applying a changed webServer.enabled/address still requires a
// process restart.
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
}

func newRuntime(cfg *config.Config, client *tileserve.Client, db *store.Store) *runtime {
	return &runtime{cfg: cfg, client: client, db: db}
}

// current returns the runtime's current config, client, and database.
func (rt *runtime) current() (*config.Config, *tileserve.Client, *store.Store) {
	rt.mu.RLock()
	defer rt.mu.RUnlock()

	return rt.cfg, rt.client, rt.db
}

// runSync runs syncAll once against the runtime's current config, client,
// and database, serialized against any other call to runSync via syncMu. It
// is what both runLoop's scheduled ticks and the web UI's manual "sync now"
// request go through, so they can't run concurrently.
func (rt *runtime) runSync(ctx context.Context, rec *status.Recorder) (int, error) {
	rt.syncMu.Lock()
	defer rt.syncMu.Unlock()

	cfg, client, db := rt.current()

	return syncAll(ctx, cfg, client, db, rec)
}

// reload re-reads configPath and, if it's valid, builds a fresh client
// (logging in again unless a token is configured) and database connection
// from it, then swaps them in. On any failure — an invalid config, a login
// failure, a database connection failure — the previous state is left in
// place and the error is returned, so a bad edit doesn't take down an
// otherwise-running sync.
//
// The previous database connection is closed only after the swap, once it's
// no longer reachable via current(). A sync already in flight at the moment
// of the swap keeps using the connection it already fetched and may see it
// close out from under it; that sync just fails and logs an error,
// self-correcting on the next iteration with the new state. That's an
// acceptable tradeoff for a manually triggered, infrequent action, rather
// than adding reference counting around every database use.
func (rt *runtime) reload(ctx context.Context, configPath string) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
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

	if closeErr := oldDB.Close(); closeErr != nil {
		log.Printf("close previous database connection: %v", closeErr)
	}

	log.Printf("config reloaded from %s", configPath)

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
