// Package syncer is the sync engine: it holds the reloadable config/client/
// database triple, schedules each configured map on its own interval, and
// runs the fetch-and-upsert passes against tileserve-go and MariaDB.
package syncer

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

// Engine holds the reloadable pieces of a sync run — the parsed config, the
// tileserve client built from it, and the database connection built from
// it — behind a mutex, so saving a config change through the config web UI
// can swap in a freshly loaded config without restarting the process.
// Current() is called
// once per sync (by RunSync, right before calling syncAll) rather than held
// for a run's whole lifetime, so a reload between syncs takes effect on the
// very next one. cfg/client/db start nil and stay nil until the first
// successful reload — see Current() — since the SQLite-backed config may
// start out empty on a fresh install. cfgDB and webServer are fixed for the
// process's lifetime: webServer settings are deliberately not reloadable
// (the server a reload request arrives on can't safely restart itself mid
// request, so applying a changed webServer.enabled/address still requires a
// process restart), and cfgDB's path is bootstrap-fixed too.
type Engine struct {
	mu     sync.RWMutex
	cfg    *config.Config
	client *tileserve.Client
	db     *store.Store

	// syncMu serializes calls to syncAll made through RunSync, so a manual
	// "sync now" request from the web UI can't run concurrently with a
	// scheduled RunLoop tick (or another manual request): two overlapping
	// syncs of the same map/version could race on pruneMissing deleting rows
	// the other's insert just wrote.
	syncMu sync.Mutex

	cfgDB     *configdb.Store
	webServer config.WebServer
	rec       *status.Recorder

	// wake is pinged by Reload() after a successful config swap, so RunLoop
	// (which otherwise sleeps for up to scheduleTick's computed duration)
	// can react immediately — e.g. syncing a newly added map right away instead
	// of waiting out whatever sleep duration was already in flight. Buffered
	// size 1 and sent to non-blockingly: at most one pending wake needs to be
	// coalesced, since RunLoop just re-reads e.Current() from scratch on
	// every tick regardless of why it woke up.
	wake chan struct{}
}

// New returns an unconfigured Engine; call Reload to load and apply the
// stored config. rec receives every sync's outcome for the status page.
func New(cfgDB *configdb.Store, webServer config.WebServer, rec *status.Recorder) *Engine {
	return &Engine{cfgDB: cfgDB, webServer: webServer, rec: rec, wake: make(chan struct{}, 1)}
}

// Current returns the engine's current config, client, and database. Any
// of the three may be nil if the engine has never had a successful reload
// — check db specifically (as RunLoop does) to tell whether the engine is
// configured yet, rather than adding a second locked call for that: false
// only until the first successful Reload() call against a valid config,
// normal (not an error) right after a fresh install with an empty
// SQLite-backed config.
func (e *Engine) Current() (*config.Config, *tileserve.Client, *store.Store) {
	e.mu.RLock()
	defer e.mu.RUnlock()

	return e.cfg, e.client, e.db
}

// ErrNotConfigured is returned by RunSync/runSyncMaps when no successful
// reload has happened yet.
var ErrNotConfigured = errors.New("not configured yet: use /config to enter and save configuration")

// RunSync runs syncAll once against every enabled map in the engine's
// current config (a Disabled map is skipped, the same way scheduleTick skips
// it for RunLoop), serialized against any other call to RunSync/runSyncMaps
// via syncMu. It is what both the run-once path (no interval configured on
// any map) and the web UI's manual "sync now" request go through.
func (e *Engine) RunSync(ctx context.Context) (int, error) {
	e.syncMu.Lock()
	defer e.syncMu.Unlock()

	cfg, client, db := e.Current()
	if db == nil {
		e.rec.RecordRun(0, ErrNotConfigured)
		return 0, ErrNotConfigured
	}

	enabled := make([]config.MapTarget, 0, len(cfg.Maps))

	for _, m := range cfg.Maps {
		if !m.Disabled {
			enabled = append(enabled, m)
		}
	}

	return syncAll(ctx, enabled, client, db, e.rec)
}

// SyncMap syncs the single map mapID immediately, regardless of its own
// schedule or Disabled flag — what the status page's per-map "Sync" button
// calls (see runSyncMaps).
func (e *Engine) SyncMap(ctx context.Context, mapID string) (int, error) {
	return e.runSyncMaps(ctx, map[string]struct{}{mapID: {}})
}

// Close closes the current database connection, if any. Call it once the
// engine is no longer in use (RunLoop/RunSync have returned).
func (e *Engine) Close() error {
	_, _, db := e.Current()
	if db == nil {
		return nil
	}

	return db.Close()
}

// runSyncMaps runs syncAll against just the maps in the engine's current
// config whose ID is in ids, serialized the same way RunSync is. It's what
// RunLoop's per-map interval scheduler calls with the set of currently-due
// map IDs (scheduleTick already excludes disabled maps from that set), and
// what the status page's per-map "Sync" button calls with a single explicit
// ID — unlike the scheduler, that explicit request is honored regardless of
// Disabled, since a user clicking "Sync" on a specific map is an intentional
// override of the map's own automatic-scheduling opt-out, not something
// scheduling decided. It re-reads e.Current() itself (rather than trusting
// maps handed to it earlier) so it always syncs each map's latest
// configured versions/staticColumns, even if a reload landed between
// RunLoop computing ids and this call acquiring syncMu.
func (e *Engine) runSyncMaps(ctx context.Context, ids map[string]struct{}) (int, error) {
	e.syncMu.Lock()
	defer e.syncMu.Unlock()

	cfg, client, db := e.Current()
	if db == nil {
		e.rec.RecordRun(0, ErrNotConfigured)
		return 0, ErrNotConfigured
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

	return syncAll(ctx, due, client, db, e.rec)
}

// DeleteMapObjects deletes every previously-synced geo_objects row for
// mapID's map_uuid scope (across all versions), serialized against
// RunSync/runSyncMaps via syncMu for the same reason those two are
// serialized against each other: a concurrent sync of that map could
// otherwise race on inserting rows this delete is in the middle of removing.
// It's a no-op (0, nil) if the engine has no successful reload yet — a map
// deleted before any sync ever ran has nothing to clean up.
//
// It also strips mapID from the engine's currently active config here,
// still under syncMu, via removeMap. This matters because the caller
// (deleteMapAPIHandler) always follows this call with Reload(), and Reload()
// only swaps in the freshly loaded (mapID-less) config if the *whole*
// config still validates and the API/database it describes are still
// reachable — e.g. a login failure or an unrelated validation error at the
// moment of this particular reload. Without stripping it here too, a failed
// reload would leave the previous, stale in-memory config — still
// containing the deleted map — active, so a later scheduled or manual sync
// would keep re-syncing it and re-inserting the very rows just purged
// above, with nothing left to prune them afterward. It also drops the map's
// results from the status recorder, so the status page stops listing it.
func (e *Engine) DeleteMapObjects(ctx context.Context, mapID string) (int64, error) {
	e.syncMu.Lock()
	defer e.syncMu.Unlock()

	_, _, db := e.Current()
	if db == nil {
		return 0, nil
	}

	deleted, err := db.DeleteMapObjects(ctx, mapID)

	e.removeMap(mapID)
	e.rec.RemoveMap(mapID)

	return deleted, err
}

// removeMap removes mapID from the engine's currently active config's Maps
// slice, if present, by swapping in a new *config.Config value with a
// filtered Maps slice rather than mutating e.cfg.Maps in place — Current()
// callers may be reading the slice they were handed without e.mu held, so
// an in-place mutation would race with them. A no-op if the engine has no
// config yet or mapID isn't in it. See DeleteMapObjects for why this exists
// alongside Reload().
func (e *Engine) removeMap(mapID string) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.cfg == nil {
		return
	}

	maps := make([]config.MapTarget, 0, len(e.cfg.Maps))

	for _, m := range e.cfg.Maps {
		if m.ID != mapID {
			maps = append(maps, m)
		}
	}

	if len(maps) == len(e.cfg.Maps) {
		return
	}

	newCfg := *e.cfg
	newCfg.Maps = maps
	e.cfg = &newCfg
}

// CreateMapOverlays keeps the EDP map_src_overlays table (see
// internal/store's CreateMapOverlays) in sync with a just-created map,
// serialized against RunSync/runSyncMaps via syncMu for the same reason
// DeleteMapObjects is. A no-op if the engine has no successful reload yet.
func (e *Engine) CreateMapOverlays(ctx context.Context, m config.MapTarget) error {
	e.syncMu.Lock()
	defer e.syncMu.Unlock()

	cfg, _, db := e.Current()
	if db == nil {
		return nil
	}

	return db.CreateMapOverlays(ctx, cfg.API.BaseURL, m)
}

// UpdateMapOverlays keeps map_src_overlays in sync with an edited map (see
// internal/store's UpdateMapOverlays), serialized the same way
// CreateMapOverlays is. A no-op if the engine has no successful reload yet.
func (e *Engine) UpdateMapOverlays(ctx context.Context, before, after config.MapTarget) error {
	e.syncMu.Lock()
	defer e.syncMu.Unlock()

	cfg, _, db := e.Current()
	if db == nil {
		return nil
	}

	return db.UpdateMapOverlays(ctx, cfg.API.BaseURL, before, after)
}

// DeleteMapOverlays removes m's map_src_overlays rows (see internal/store's
// DeleteMapOverlays), serialized the same way CreateMapOverlays is. A no-op
// if the engine has no successful reload yet.
func (e *Engine) DeleteMapOverlays(ctx context.Context, m config.MapTarget) error {
	e.syncMu.Lock()
	defer e.syncMu.Unlock()

	cfg, _, db := e.Current()
	if db == nil {
		return nil
	}

	return db.DeleteMapOverlays(ctx, cfg.API.BaseURL, m)
}

// Reload loads the current configdb-backed config, overlays the fixed
// bootstrap WebServer, validates it, and — only if that succeeds — builds a
// fresh client (logging in again unless a token is configured) and database
// connection, then swaps them in. On any failure — an invalid config, a
// login failure, a database connection failure — the previous state (which
// may be the initial nil state) is left in place and the error is returned,
// so a bad edit doesn't take down an otherwise-running sync.
//
// The previous database connection is closed only after the swap, once it's
// no longer reachable via Current() (nil on the very first successful
// reload from an empty state). A sync already in flight at the moment of
// the swap keeps using the connection it already fetched and may see it
// close out from under it; that sync just fails and logs an error,
// self-correcting on the next iteration with the new state. That's an
// acceptable tradeoff for a manually triggered, infrequent action, rather
// than adding reference counting around every database use.
func (e *Engine) Reload(ctx context.Context) error {
	cfg, err := e.cfgDB.Load(ctx)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	cfg.WebServer = e.webServer

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

	e.mu.Lock()
	oldDB := e.db
	e.cfg, e.client, e.db = cfg, client, db
	e.mu.Unlock()

	if oldDB != nil {
		if closeErr := oldDB.Close(); closeErr != nil {
			log.Printf("close previous database connection: %v", closeErr)
		}
	}

	log.Print("config reloaded")

	select {
	case e.wake <- struct{}{}:
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
