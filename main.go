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
	configPath := flag.String("config", "config.yaml", "path to the YAML config file")
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
	cfg, err := config.Load(configPath)
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

	log.SetOutput(io.MultiWriter(writers...))

	if cfg.WebServer.Enabled {
		stopWebServer := startWebServer(cfg.WebServer.Address, rec)
		defer stopWebServer()
	}

	client := tileserve.New(cfg.API.BaseURL)
	if cfg.API.Token != "" {
		client.SetToken(cfg.API.Token)
	} else {
		log.Printf("logging in to %s as %s", cfg.API.BaseURL, cfg.API.Username)

		if err := client.Login(ctx, cfg.API.Username, cfg.API.Password); err != nil {
			return fmt.Errorf("login: %w", err)
		}
	}

	db, err := store.Open(ctx, cfg.Database, cfg.StaticColumnNames())
	if err != nil {
		return fmt.Errorf("connect to database: %w", err)
	}
	defer func() { _ = db.Close() }()

	if err := db.EnsureSchema(ctx); err != nil {
		return err
	}

	interval := cfg.SyncInterval()
	if interval <= 0 {
		return syncAll(ctx, cfg, client, db, rec)
	}

	return runLoop(ctx, cfg, client, db, interval, rec)
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

// startWebServer starts the status/log web server in a goroutine and
// returns a function that shuts it down; run defers a call to it, so the
// server stops when run returns (including on ctx cancellation, since that's
// what ends syncAll/runLoop). Listen errors (other than a clean shutdown) are
// logged rather than returned, since a status page failing to start
// shouldn't stop the sync itself.
func startWebServer(addr string, rec *status.Recorder) (stop func()) {
	srv := webserver.New(addr, rec)

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

// runLoop runs syncAll immediately, then repeats it every interval until ctx
// is cancelled (e.g. by SIGINT/SIGTERM). Errors from an individual sync are
// logged rather than aborting the loop, so a transient failure (e.g. a
// network blip) doesn't take down an otherwise long-running process.
func runLoop(
	ctx context.Context,
	cfg *config.Config,
	client *tileserve.Client,
	db *store.Store,
	interval time.Duration,
	rec *status.Recorder,
) error {
	log.Printf("running sync every %s (press Ctrl+C to stop)", interval)

	for {
		if err := syncAll(ctx, cfg, client, db, rec); err != nil {
			log.Printf("sync error: %v", err)
		}

		select {
		case <-ctx.Done():
			log.Print("shutting down")
			return nil
		case <-time.After(interval):
		}
	}
}

// syncAll fetches and upserts geo objects for every configured map/version
// pair once, recording each pair's outcome and the run as a whole in rec for
// the status web server to display.
func syncAll(
	ctx context.Context, cfg *config.Config, client *tileserve.Client, db *store.Store, rec *status.Recorder,
) (err error) {
	var totalSynced int

	defer func() { rec.RecordRun(totalSynced, err) }()

	for _, m := range cfg.Maps {
		for _, version := range m.Versions {
			log.Printf("fetching geo objects for map %s version %s", m.ID, version)

			objects, fetchErr := client.GeoObjects(ctx, m.ID, version)
			if fetchErr != nil {
				err = fmt.Errorf("fetch geo objects for map %s version %s: %w", m.ID, version, fetchErr)
				rec.RecordMapVersion(m.ID, version, 0, err)

				return err
			}

			// Store the configured version (which may be "current" or a
			// user-defined alias) rather than whatever concrete version the
			// API resolved it to and echoed back on each object, so the
			// version column always matches what's in config.yaml.
			for i := range objects {
				objects[i].Version = version
			}

			if storeErr := db.UpsertGeoObjects(ctx, objects, m.StaticColumns, m.ID, version); storeErr != nil {
				err = fmt.Errorf("store geo objects for map %s version %s: %w", m.ID, version, storeErr)
				rec.RecordMapVersion(m.ID, version, 0, err)

				return err
			}

			log.Printf("synced %d geo object(s) for map %s version %s", len(objects), m.ID, version)
			rec.RecordMapVersion(m.ID, version, len(objects), nil)
			totalSynced += len(objects)
		}
	}

	log.Printf("done: synced %d geo object(s) total", totalSynced)

	return nil
}
