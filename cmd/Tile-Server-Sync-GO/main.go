// Command Tile-Server-Sync-GO fetches geo objects for one or more tileserve-go
// maps (and given versions) and writes them into a MariaDB database.
//
// See https://github.com/Nils-witt/Tileserve-GO/blob/main/internal/handler/openapi.yaml
// for the API this talks to.
package main

import (
	"Tile-Server-Sync-GO/internal/config"
	"Tile-Server-Sync-GO/internal/configdb"
	"Tile-Server-Sync-GO/internal/status"
	"Tile-Server-Sync-GO/internal/store"
	"Tile-Server-Sync-GO/internal/tileserve"
	"Tile-Server-Sync-GO/internal/webserver"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
)

// version, commit, and date are set via -ldflags at build time by GoReleaser
// (see .goreleaser.yaml); they stay at these defaults for `go build`/`go run`.
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	configPath := flag.String("config", "config.yaml",
		"path to the bootstrap YAML file (webServer + configDb; everything else is edited via the /config web UI)")
	showVersion := flag.Bool("version", false, "print version information and exit")
	serviceCmd := flag.String("service", "",
		"Windows service control: install, uninstall, start, stop, or run (Windows only)")

	flag.Parse()

	if *showVersion {
		fmt.Printf("Tile-Server-Sync-GO %s (commit %s, built %s)\n", version, commit, date)
		return
	}

	if *serviceCmd != "" {
		if err := handleServiceCommand(*serviceCmd, *configPath); err != nil {
			log.Fatalf("service %s: %v", *serviceCmd, err)
		}

		return
	}

	// A service installed via `-service install` is launched by the SCM
	// with `-service run` already on its command line, so this only
	// matters as a fallback if the SCM ever invokes the exe without args.
	if isService, err := isWindowsService(); err == nil && isService {
		if err := runAsService(*configPath); err != nil {
			log.Fatalf("error: %v", err)
		}

		return
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)

	err := run(ctx, *configPath)

	stop()

	if err != nil {
		log.Fatalf("error: %v", err)
	}
}

// handleServiceCommand dispatches `-service <cmd>` to the platform-specific
// implementations in service_windows.go (real Windows SCM integration) or
// service_other.go (stubs that report the feature is Windows-only).
func handleServiceCommand(cmd, configPath string) error {
	switch cmd {
	case "install":
		return installService(configPath)
	case "uninstall":
		return uninstallService()
	case "start":
		return startService()
	case "stop":
		return stopService()
	case "run":
		return runAsService(configPath)
	default:
		return fmt.Errorf("unknown -service value %q (want install, uninstall, start, stop, or run)", cmd)
	}
}

func run(ctx context.Context, configPath string) error {
	boot, err := config.LoadBootstrap(configPath)
	if err != nil {
		return err
	}

	rec := status.New()

	writers := []io.Writer{log.Writer(), rec}

	// logFile is intentionally left open for the lifetime of the process
	// (the OS closes it on exit) rather than deferred-closed here: run's
	// caller logs a final fatal error, if any, after run returns, and that
	// message should still reach the log file.
	logFile, err := openLogFile(configPath)
	if err != nil {
		log.Printf("open log file: %v", err)
	} else {
		writers = append(writers, logFile)
	}

	log.SetOutput(fanoutWriter(writers))

	cfgDB, err := configdb.Open(ctx, boot.ConfigDB)
	if err != nil {
		return fmt.Errorf("open config database: %w", err)
	}
	defer func() { _ = cfgDB.Close() }()

	rt := newRuntime(cfgDB, boot.WebServer)

	initialErr := rt.reload(ctx)
	if initialErr != nil {
		if !boot.WebServer.Enabled {
			// No web UI to fix it through — fail hard, same as today's
			// behavior for an invalid/missing config file.
			return fmt.Errorf("initial config: %w", initialErr)
		}

		log.Printf("starting with no valid configuration yet (%v); use /config to enter and save it", initialErr)
	}

	defer func() {
		_, _, db := rt.current()
		if db != nil {
			_ = db.Close()
		}
	}()

	if boot.WebServer.Enabled {
		reload := func(reloadCtx context.Context) error { return rt.reload(reloadCtx) }
		syncMap := func(syncCtx context.Context, mapID string) (int, error) {
			return rt.runSyncMaps(syncCtx, rec, map[string]struct{}{mapID: {}})
		}
		deleteMapObjects := func(delCtx context.Context, mapID string) (int64, error) {
			return rt.deleteMapObjects(delCtx, mapID)
		}
		createMapOverlays := func(ovCtx context.Context, m config.MapTarget) error {
			return rt.createMapOverlays(ovCtx, m)
		}
		updateMapOverlays := func(ovCtx context.Context, before, after config.MapTarget) error {
			return rt.updateMapOverlays(ovCtx, before, after)
		}
		deleteMapOverlays := func(ovCtx context.Context, m config.MapTarget) error {
			return rt.deleteMapOverlays(ovCtx, m)
		}

		stopWebServer := startWebServer(
			boot.WebServer.Address, rec, cfgDB, boot.WebServer, reload, syncMap, deleteMapObjects,
			createMapOverlays, updateMapOverlays, deleteMapOverlays,
		)
		defer stopWebServer()

		// Since config may start out empty/invalid and only become valid
		// (with or without an interval) via a later live edit, the process
		// stays alive and polling as long as the web server is enabled,
		// rather than choosing once at startup between "run once and exit"
		// and "loop forever" the way the no-web-server branch below still
		// does.
		return runLoop(ctx, rt, rec)
	}

	// Config is guaranteed valid here (initialErr == nil, or we'd have
	// returned above), so this preserves today's exact behavior.
	cfg, _, _ := rt.current()
	if !cfg.HasRecurringMaps() {
		_, err := rt.runSync(ctx, rec)
		return err
	}

	return runLoop(ctx, rt, rec)
}

// fanoutWriter writes p to every writer in the slice, independently of
// whether earlier writers error. Unlike io.MultiWriter, which stops at the
// first failing writer, this makes sure a broken sink (e.g. stderr under a
// Windows service, which has no console and can fail on write) can't starve
// the others, such as the in-memory recorder the status web server reads
// from or the log file.
type fanoutWriter []io.Writer

func (f fanoutWriter) Write(p []byte) (int, error) {
	for _, w := range f {
		_, _ = w.Write(p)
	}

	return len(p), nil
}

// openLogFile opens (creating if needed, appending if not) a
// Tile-Server-Sync-GO.log file next to the config file at configPath, so logs
// land alongside the config that produced them rather than wherever the
// process happens to be run from (e.g. the Windows service's working
// directory).
func openLogFile(configPath string) (*os.File, error) {
	logPath := filepath.Join(filepath.Dir(configPath), "Tile-Server-Sync-GO.log")

	f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600) //nolint:gosec // configPath is a trusted, user-supplied CLI flag
	if err != nil {
		return nil, fmt.Errorf("open %q: %w", logPath, err)
	}

	return f, nil
}

// startWebServer starts the status/log/config web server in a goroutine and
// returns a function that shuts it down; run defers a call to it, so the
// server stops when run returns (including on ctx cancellation, since that's
// what ends syncAll/runLoop). Listen errors (other than a clean shutdown) are
// logged rather than returned, since a status page failing to start
// shouldn't stop the sync itself.
func startWebServer(
	addr string, rec *status.Recorder, cfgDB *configdb.Store, webServer config.WebServer,
	reload func(context.Context) error, syncMap func(context.Context, string) (int, error),
	deleteMapObjects func(context.Context, string) (int64, error),
	createMapOverlays func(context.Context, config.MapTarget) error,
	updateMapOverlays func(context.Context, config.MapTarget, config.MapTarget) error,
	deleteMapOverlays func(context.Context, config.MapTarget) error,
) (stop func()) {
	srv := webserver.New(
		addr, rec, cfgDB, webServer, version, commit, reload, syncMap, deleteMapObjects,
		createMapOverlays, updateMapOverlays, deleteMapOverlays,
	)

	go func() {
		log.Printf("status web server listening on %s", addr)

		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("status web server error: %v", err)
		}
	}()

	return func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		_ = srv.Shutdown(shutdownCtx)
	}
}

// pollInterval is how often runLoop checks back in while the runtime isn't
// configured yet (fresh install, empty SQLite-backed config), or is
// configured but none of its maps have a positive Interval yet due, waiting
// for a live edit via the web UI to make an automatic sync possible.
const pollInterval = 5 * time.Second

// runLoop syncs each configured map on its own schedule — determined by that
// map's own Interval (see config.MapTarget) — until ctx is cancelled (e.g. by
// SIGINT/SIGTERM). Every map is synced once immediately the first time it's
// seen (covering both startup and a map added later via a live config
// reload); a map with a positive Interval then keeps re-syncing every
// Interval after that, while a map with no Interval is not automatically
// repeated. Errors from an individual sync are logged rather than aborting
// the loop, so a transient failure (e.g. a network blip) doesn't take down an
// otherwise long-running process.
//
// Each tick re-fetches rt.current() (rather than this loop capturing it
// once), so a config reload triggered via the web UI — new/removed maps,
// changed intervals, API credentials, database settings — takes effect
// immediately: rt.reload pings rt.wake on success, which this loop also
// selects on, so it doesn't wait out whatever wait duration was already in
// flight (which could otherwise be as long as another map's interval).
// rt.runSyncMaps also serializes against a manual "sync now" request from the
// web UI, so the two can't run concurrently. lastSync (map ID -> last sync
// start time) is purely in-memory scheduling state for this run of the loop;
// it doesn't survive a restart, so every map syncs once immediately whenever
// the process starts.
func runLoop(ctx context.Context, rt *runtime, rec *status.Recorder) error {
	lastSync := make(map[string]time.Time)

	log.Print("running (press Ctrl+C to stop)")

	for {
		wait := tick(ctx, rt, rec, lastSync)

		select {
		case <-ctx.Done():
			log.Print("shutting down")
			return nil
		case <-time.After(wait):
		case <-rt.wake:
			// A config reload landed (e.g. a map added/edited via /config) —
			// loop back around immediately instead of finishing out the wait
			// computed from the config that's now stale, so a newly added
			// map gets synced right away rather than after whatever sleep
			// happened to already be in flight.
		}
	}
}

// tick runs one pass of runLoop's schedule: syncing whatever maps are
// currently due (if the runtime is configured yet) and returning how long
// runLoop should sleep before its next tick. lastSync is mutated in place
// for every map synced this tick.
func tick(ctx context.Context, rt *runtime, rec *status.Recorder, lastSync map[string]time.Time) time.Duration {
	cfg, _, db := rt.current()
	if db == nil {
		log.Print("waiting for configuration via /config")
		return pollInterval
	}

	now := time.Now()

	due, dueIntervals, wait, haveWait := scheduleTick(cfg.Maps, lastSync, now)
	if !haveWait {
		wait = pollInterval
	}

	if len(due) == 0 {
		return wait
	}

	syncStart := now

	if _, err := rt.runSyncMaps(ctx, rec, due); err != nil {
		log.Printf("sync error: %v", err)
	}

	// Fold each just-synced map's own next-due time (syncStart + its
	// Interval) into wait, rather than re-scanning every configured map a
	// second time the way separate due/wait passes used to: this is the
	// only part of the schedule that actually changed by syncing, so it's
	// the only part recomputed here. completed (rather than syncStart) is
	// what remaining is measured against, so a slow sync doesn't shorten
	// the map's next interval — its next due time stays pinned to
	// syncStart+Interval either way.
	completed := time.Now()

	for id := range due {
		lastSync[id] = syncStart

		interval, ok := dueIntervals[id]
		if !ok {
			continue
		}

		remaining := max(interval-completed.Sub(syncStart), 0)
		if !haveWait || remaining < wait {
			wait, haveWait = remaining, true
		}
	}

	return wait
}

// scheduleTick computes, in one pass over maps, which map IDs are due to
// sync right now (a map with no lastSync entry yet is always due, once;
// after that, a map with a positive Interval is due again once that much
// time has passed since its last sync, while a map with no Interval is
// never due again automatically) together with otherWait/haveWait: the
// shortest remaining time until any *not-due* map with a positive Interval
// next comes due (haveWait is false if there's no such map — nothing
// configured yet, every configured map is one-shot or already due, or none
// has synced yet).
//
// dueIntervals carries the positive Interval of every due map, so runLoop
// can fold each just-synced map's fresh next-due time into otherWait after
// syncing without a second pass over every configured map — mirroring what
// the former separate dueMaps/nextWake functions computed in two full
// passes (nextWake's, run after sync, always re-scanned every map — due and
// not — a second time).
func scheduleTick(maps []config.MapTarget, lastSync map[string]time.Time, now time.Time) (
	due map[string]struct{}, dueIntervals map[string]time.Duration, otherWait time.Duration, haveWait bool,
) {
	for _, m := range maps {
		last, seen := lastSync[m.ID]
		interval := m.SyncInterval()

		switch {
		case !seen, interval > 0 && now.Sub(last) >= interval:
			if due == nil {
				due = make(map[string]struct{}, len(maps))
			}

			due[m.ID] = struct{}{}

			if interval > 0 {
				if dueIntervals == nil {
					dueIntervals = make(map[string]time.Duration, len(maps))
				}

				dueIntervals[m.ID] = interval
			}
		case interval > 0:
			remaining := interval - now.Sub(last)
			if !haveWait || remaining < otherWait {
				otherWait, haveWait = remaining, true
			}
		}
	}

	return due, dueIntervals, otherWait, haveWait
}

// syncAll fetches and upserts geo objects for every map/version pair in maps
// once, recording each pair's outcome and the run as a whole in rec for the
// status web server to display, and returns the total number of objects
// synced across every pair that succeeded. maps is not necessarily every map
// in the current config — runLoop passes only the maps currently due for a
// sync, per their own Interval, while the run-once path and the web UI's
// "sync now" pass every configured map.
//
// A failure on one map/version (a fetch error or a store error) is logged
// and recorded against that pair, but does not stop the others from being
// attempted — one map being unreachable or misconfigured shouldn't prevent
// the rest of the fleet from syncing. If any pair failed, syncAll still
// returns a non-nil error (joining every failure) after all pairs have been
// attempted, so callers (runLoop's logging, the run-once path, the web UI's
// "sync now") can tell the run as a whole was not fully successful.
func syncAll(
	ctx context.Context, maps []config.MapTarget, client *tileserve.Client, db *store.Store, rec *status.Recorder,
) (totalSynced int, err error) {
	defer func() { rec.RecordRun(totalSynced, err) }()

	var errs []error

	for _, m := range maps {
		for _, version := range m.Versions {
			log.Printf("fetching geo objects for map %s version %s", m.ID, version)

			objects, fetchErr := client.GeoObjects(ctx, m.ID, version)
			if fetchErr != nil {
				pairErr := fmt.Errorf("fetch geo objects for map %s version %s: %w", m.ID, version, fetchErr)
				log.Printf("sync error: %v", pairErr)
				rec.RecordMapVersion(m.ID, version, 0, pairErr)
				errs = append(errs, pairErr)

				continue
			}

			// Store the configured version (which may be "current" or a
			// user-defined alias) rather than whatever concrete version the
			// API resolved it to and echoed back on each object, so the
			// version column always matches what's in config.yaml.
			for i := range objects {
				objects[i].Version = version
			}

			if storeErr := db.UpsertGeoObjects(ctx, objects, m.StaticColumns, m.ID, version); storeErr != nil {
				pairErr := fmt.Errorf("store geo objects for map %s version %s: %w", m.ID, version, storeErr)
				log.Printf("sync error: %v", pairErr)
				rec.RecordMapVersion(m.ID, version, 0, pairErr)
				errs = append(errs, pairErr)

				continue
			}

			log.Printf("synced %d geo object(s) for map %s version %s", len(objects), m.ID, version)
			rec.RecordMapVersion(m.ID, version, len(objects), nil)
			totalSynced += len(objects)
		}
	}

	log.Printf("done: synced %d geo object(s) total", totalSynced)

	return totalSynced, errors.Join(errs...)
}
