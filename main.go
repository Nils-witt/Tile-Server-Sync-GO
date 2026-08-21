// Command go-sync-objects fetches geo objects for one or more tileserve-go
// maps (and given versions) and writes them into a MariaDB database.
//
// See https://github.com/Nils-witt/Tileserve-GO/blob/main/internal/handler/openapi.yaml
// for the API this talks to.
package main

import (
	"context"
	"flag"
	"fmt"
	"go-sync-objects/internal/config"
	"go-sync-objects/internal/store"
	"go-sync-objects/internal/tileserve"
	"log"
	"os"
	"os/signal"
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

	flag.Parse()

	if *showVersion {
		fmt.Printf("go-sync-objects %s (commit %s, built %s)\n", version, commit, date)
		return
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)

	err := run(ctx, *configPath)

	stop()

	if err != nil {
		log.Fatalf("error: %v", err)
	}
}

func run(ctx context.Context, configPath string) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
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
		return syncAll(ctx, cfg, client, db)
	}

	return runLoop(ctx, cfg, client, db, interval)
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
) error {
	log.Printf("running sync every %s (press Ctrl+C to stop)", interval)

	for {
		if err := syncAll(ctx, cfg, client, db); err != nil {
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
// pair once.
func syncAll(ctx context.Context, cfg *config.Config, client *tileserve.Client, db *store.Store) error {
	var totalSynced int

	for _, m := range cfg.Maps {
		for _, version := range m.Versions {
			log.Printf("fetching geo objects for map %s version %s", m.ID, version)

			objects, err := client.GeoObjects(ctx, m.ID, version)
			if err != nil {
				return fmt.Errorf("fetch geo objects for map %s version %s: %w", m.ID, version, err)
			}

			// Store the configured version (which may be "current" or a
			// user-defined alias) rather than whatever concrete version the
			// API resolved it to and echoed back on each object, so the
			// version column always matches what's in config.yaml.
			for i := range objects {
				objects[i].Version = version
			}

			if err := db.UpsertGeoObjects(ctx, objects, m.StaticColumns, m.ID, version); err != nil {
				return fmt.Errorf("store geo objects for map %s version %s: %w", m.ID, version, err)
			}

			log.Printf("synced %d geo object(s) for map %s version %s", len(objects), m.ID, version)
			totalSynced += len(objects)
		}
	}

	log.Printf("done: synced %d geo object(s) total", totalSynced)

	return nil
}
