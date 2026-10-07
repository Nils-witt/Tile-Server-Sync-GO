// Command Tile-Server-Sync-GO fetches geo objects for one or more tileserve-go
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
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/Nils-witt/Tile-Server-Sync-GO/internal/config"
	"github.com/Nils-witt/Tile-Server-Sync-GO/internal/configdb"
	"github.com/Nils-witt/Tile-Server-Sync-GO/internal/status"
	"github.com/Nils-witt/Tile-Server-Sync-GO/internal/syncer"
	"github.com/Nils-witt/Tile-Server-Sync-GO/internal/webserver"
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

	eng := syncer.New(cfgDB, boot.WebServer, rec)
	defer func() { _ = eng.Close() }()

	if err := eng.Reload(ctx); err != nil {
		log.Printf("starting with no valid configuration yet (%v); use /config to enter and save it", err)
	}

	stopWebServer := startWebServer(webserver.Options{
		Addr:     boot.WebServer.Address,
		Recorder: rec,
		ConfigDB: cfgDB,
		SSO:      boot.SSO,
		Version:  version,
		Commit:   commit,
		Engine:   eng,
	})
	defer stopWebServer()

	// Config may start out empty/invalid and only become valid via a later
	// live edit, so the process always stays alive and polling.
	return eng.RunLoop(ctx)
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
// what ends RunLoop). Listen errors (other than a clean shutdown) are
// logged rather than returned, since a status page failing to start
// shouldn't stop the sync itself.
func startWebServer(opts webserver.Options) (stop func()) {
	srv := webserver.New(opts)

	go func() {
		log.Printf("status web server listening on %s", opts.Addr)

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
