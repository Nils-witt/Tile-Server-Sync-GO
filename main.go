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
)

func main() {
	configPath := flag.String("config", "config.yaml", "path to the YAML config file")

	flag.Parse()

	if err := run(context.Background(), *configPath); err != nil {
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

	db, err := store.Open(ctx, cfg.Database)
	if err != nil {
		return fmt.Errorf("connect to database: %w", err)
	}
	defer func() { _ = db.Close() }()

	if err := db.EnsureSchema(ctx); err != nil {
		return err
	}

	var totalSynced int

	for _, m := range cfg.Maps {
		for _, version := range m.Versions {
			log.Printf("fetching geo objects for map %s version %s", m.ID, version)

			objects, err := client.GeoObjects(ctx, m.ID, version)
			if err != nil {
				return fmt.Errorf("fetch geo objects for map %s version %s: %w", m.ID, version, err)
			}

			if err := db.UpsertGeoObjects(ctx, objects); err != nil {
				return fmt.Errorf("store geo objects for map %s version %s: %w", m.ID, version, err)
			}

			log.Printf("synced %d geo object(s) for map %s version %s", len(objects), m.ID, version)
			totalSynced += len(objects)
		}
	}

	log.Printf("done: synced %d geo object(s) total", totalSynced)

	return nil
}
