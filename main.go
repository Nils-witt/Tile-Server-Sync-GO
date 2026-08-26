// Command go-sync-objects fetches geo objects for one or more tileserve-go
// maps (and given versions) and writes them into a MariaDB database.
//
// See https://github.com/Nils-witt/Tileserve-GO/blob/main/internal/handler/openapi.yaml
// for the API this talks to.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"go-sync-objects/internal/config"
	"go-sync-objects/internal/configdb"
	"go-sync-objects/internal/status"
	"go-sync-objects/internal/store"
	"go-sync-objects/internal/tileserve"
	"go-sync-objects/internal/webserver"
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
		fmt.Printf("go-sync-objects %s (commit %s, built %s)\n", version, commit, date)
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

		log.Printf("starting with no valid configuration yet (%v); use /config to enter it, then POST /api/reload",
			initialErr)
	}

	defer func() {
		_, _, db := rt.current()
		if db != nil {
			_ = db.Close()
		}
	}()

	if boot.WebServer.Enabled {
		reload := func(reloadCtx context.Context) error { return rt.reload(reloadCtx) }
		syncNow := func(syncCtx context.Context) (int, error) { return rt.runSync(syncCtx, rec) }

		stopWebServer := startWebServer(boot.WebServer.Address, rec, cfgDB, boot.WebServer, reload, syncNow)
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
	if cfg.SyncInterval() <= 0 {
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
// go-sync-objects.log file next to the config file at configPath, so logs
// land alongside the config that produced them rather than wherever the
// process happens to be run from (e.g. the Windows service's working
// directory).
func openLogFile(configPath string) (*os.File, error) {
	logPath := filepath.Join(filepath.Dir(configPath), "go-sync-objects.log")

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
	reload func(context.Context) error, syncNow func(context.Context) (int, error),
) (stop func()) {
	srv := webserver.New(addr, rec, cfgDB, webServer, reload, syncNow)

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
// configured yet (fresh install, empty SQLite-backed config) or is
// configured but has no interval set, waiting for a live edit via the web
// UI (or its interval) to make an automatic sync possible.
const pollInterval = 5 * time.Second

// runLoop runs syncAll immediately (once the runtime is configured), then
// repeats it every interval until ctx is cancelled (e.g. by SIGINT/SIGTERM).
// Errors from an individual sync are logged rather than aborting the loop,
// so a transient failure (e.g. a network blip) doesn't take down an
// otherwise long-running process.
//
// rt.runSync re-fetches rt.current() at the top of every call (rather than
// this loop capturing it once), so a config reload triggered via the web
// UI — new maps, API credentials, database settings, or interval — takes
// effect on the very next sync without a process restart. runSync also
// serializes against a manual "sync now" request from the web UI, so the two
// can't run concurrently. If a reload leaves no interval configured, the
// loop keeps using the last positive interval it saw rather than
// busy-looping; if the runtime isn't configured at all yet (or has never
// had a positive interval), it polls every pollInterval instead, so a fresh
// install's web UI can go from "no config" to "syncing" without a restart.
func runLoop(ctx context.Context, rt *runtime, rec *status.Recorder) error {
	var lastInterval time.Duration

	log.Print("running (press Ctrl+C to stop)")

	for {
		if rt.configured() {
			if _, err := rt.runSync(ctx, rec); err != nil {
				log.Printf("sync error: %v", err)
			}

			if cfg, _, _ := rt.current(); cfg.SyncInterval() > 0 {
				lastInterval = cfg.SyncInterval()
			}
		} else {
			log.Print("waiting for configuration via /config, then POST /api/reload")
		}

		wait := lastInterval
		if wait <= 0 {
			wait = pollInterval
		}

		select {
		case <-ctx.Done():
			log.Print("shutting down")
			return nil
		case <-time.After(wait):
		}
	}
}

// syncAll fetches and upserts geo objects for every configured map/version
// pair once, recording each pair's outcome and the run as a whole in rec for
// the status web server to display, and returns the total number of objects
// synced across every pair that succeeded.
//
// A failure on one map/version (a fetch error or a store error) is logged
// and recorded against that pair, but does not stop the others from being
// attempted — one map being unreachable or misconfigured shouldn't prevent
// the rest of the fleet from syncing. If any pair failed, syncAll still
// returns a non-nil error (joining every failure) after all pairs have been
// attempted, so callers (runLoop's logging, the run-once path, the web UI's
// "sync now") can tell the run as a whole was not fully successful.
func syncAll(
	ctx context.Context, cfg *config.Config, client *tileserve.Client, db *store.Store, rec *status.Recorder,
) (totalSynced int, err error) {
	defer func() { rec.RecordRun(totalSynced, err) }()

	var errs []error

	for _, m := range cfg.Maps {
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
