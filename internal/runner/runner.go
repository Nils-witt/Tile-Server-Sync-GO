// Package runner is the application's central coordinator: it owns the
// configuration (loading it from and saving it to the SQLite config
// database), applies it live by building the tileserve client and MariaDB
// connection it describes, schedules each configured map on its own
// interval, and runs the fetch-and-upsert passes. Everything else — most
// notably internal/webserver — only reads state from a *Runner or asks it to
// make a change; nothing outside this package touches configdb directly.
package runner

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"

	"github.com/Nils-witt/Tile-Server-Sync-GO/internal/config"
	"github.com/Nils-witt/Tile-Server-Sync-GO/internal/configdb"
	"github.com/Nils-witt/Tile-Server-Sync-GO/internal/status"
	"github.com/Nils-witt/Tile-Server-Sync-GO/internal/store"
	"github.com/Nils-witt/Tile-Server-Sync-GO/internal/tileserve"
)

// Runner holds two views of the configuration:
//
//   - stored: what is saved in configdb, exactly as last written, which
//     may be incomplete or invalid (e.g. during initial setup, when only
//     the API tab has been saved yet). This is what the config UI shows and
//     edits. It's kept in memory, loaded once by New, and updated after
//     every successful save, so reads never hit SQLite.
//   - active: the validated config plus the tileserve client and database
//     connection built from it, which syncs actually run against. It
//     starts out nil and is only replaced by a successful apply, so an
//     invalid edit or an unreachable API/database leaves the previous,
//     still-working state in place.
//
// webServer is fixed for the process's lifetime: the server a change request
// arrives on can't safely restart itself mid-request, so a changed
// webServer.address always needs a process restart — which is also why it
// lives in the bootstrap file rather than configdb.
type Runner struct {
	cfgDB     *configdb.Store
	webServer config.WebServer
	rec       *status.Recorder

	// writeMu serializes every change (save + apply, map CRUD), so two
	// concurrent edits can't interleave their read-modify-write of stored
	// or race each other's apply.
	writeMu sync.Mutex

	// mu guards stored and the active triple. It's only held for the
	// pointer swaps/copies themselves, never across I/O.
	mu     sync.RWMutex
	stored *config.Config
	cfg    *config.Config
	client *tileserve.Client
	db     *store.Store

	// syncMu serializes syncs (and the per-map cleanup/overlay writes that
	// touch the same MariaDB tables), so a manual "sync now" request can't
	// run concurrently with a scheduled tick: two overlapping syncs of the
	// same map/version could race on pruneMissing deleting rows the other's
	// insert just wrote.
	syncMu sync.Mutex

	// wake is pinged after a successful apply, so Run (which otherwise
	// sleeps for up to scheduleTick's computed duration) reacts immediately
	// — e.g. syncing a newly added map right away. Buffered size 1 and sent
	// to non-blockingly: Run re-reads the active config from scratch on
	// every tick regardless of why it woke up.
	wake chan struct{}
}

// Actor identifies who asked for a change, for the security log.
type Actor struct {
	Username   string
	RemoteAddr string
}

// ChangeResult reports the side effects of a change that was successfully
// saved. ApplyErr is set when the saved config couldn't be applied live (it
// is invalid or incomplete, or its API/database is unreachable) — the save
// itself is never rolled back for that. OverlayErr reports a failure to keep
// EDP's map_src_overlays in sync; ObjectsDeleted/ObjectsErr report the
// purge of a deleted map's synced rows.
type ChangeResult struct {
	ApplyErr       error
	OverlayErr     error
	ObjectsDeleted int64
	ObjectsErr     error
}

var (
	// ErrNotConfigured is returned by a sync request before any config has
	// been successfully applied.
	ErrNotConfigured = errors.New("not configured yet: use /config to enter and save configuration")
	// ErrInvalid wraps a validation failure of a requested change.
	ErrInvalid = errors.New("invalid")
	// ErrMapNotFound is returned by the map methods for an unknown map id.
	ErrMapNotFound = configdb.ErrMapNotFound
	// ErrMapIDTaken is returned by CreateMap for an id already in use.
	ErrMapIDTaken = configdb.ErrMapIDTaken
)

// New loads the stored config from cfgDB and returns a Runner holding it,
// not yet applied: Run applies it before it starts scheduling. rec receives
// every sync's outcome for the status page.
func New(ctx context.Context, cfgDB *configdb.Store, webServer config.WebServer, rec *status.Recorder) (*Runner, error) {
	stored, err := cfgDB.Load(ctx)
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}

	stored.WebServer = webServer

	return &Runner{
		cfgDB: cfgDB, webServer: webServer, rec: rec, stored: stored, wake: make(chan struct{}, 1),
	}, nil
}

// Status returns a snapshot of sync results and recent log output.
func (r *Runner) Status() status.Snapshot {
	return r.rec.Snapshot()
}

// Close closes the active database connection, if any. Call it once Run
// has returned.
func (r *Runner) Close() error {
	_, _, db := r.active()
	if db == nil {
		return nil
	}

	return db.Close()
}

// active returns the applied config, client, and database. All three are
// nil until the first successful apply — check db to tell whether the
// runner is configured yet.
func (r *Runner) active() (*config.Config, *tileserve.Client, *store.Store) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	return r.cfg, r.client, r.db
}

// storedCopy returns a deep copy of the stored config, safe for the caller
// to modify.
func (r *Runner) storedCopy() *config.Config {
	r.mu.RLock()
	defer r.mu.RUnlock()

	return cloneConfig(r.stored)
}

// setStored replaces the stored config. cfg must not be modified afterwards.
func (r *Runner) setStored(cfg *config.Config) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.stored = cfg
}

// apply validates the stored config and — only if that succeeds — builds a
// fresh client (logging in again unless a token is configured) and database
// connection, then swaps them in as the active state. On any failure the
// previous active state (which may be the initial nil state) is left in
// place and the error is returned.
//
// The previous database connection is closed only after the swap. A sync
// already in flight at that moment keeps using the connection it fetched
// and may see it close out from under it; that sync just fails and logs an
// error, self-correcting on the next tick. That's an acceptable tradeoff for
// an infrequent, manually triggered action, rather than reference counting
// every database use.
func (r *Runner) apply(ctx context.Context) error {
	cfg := r.storedCopy()
	cfg.WebServer = r.webServer

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

	r.mu.Lock()
	oldDB := r.db
	r.cfg, r.client, r.db = cfg, client, db
	r.mu.Unlock()

	if oldDB != nil {
		if closeErr := oldDB.Close(); closeErr != nil {
			log.Printf("close previous database connection: %v", closeErr)
		}
	}

	log.Print("config applied")

	select {
	case r.wake <- struct{}{}:
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
