package runner

import (
	"context"
	"errors"
	"fmt"
	"log"

	"github.com/Nils-witt/Tile-Server-Sync-GO/internal/config"
	"github.com/Nils-witt/Tile-Server-Sync-GO/internal/status"
	"github.com/Nils-witt/Tile-Server-Sync-GO/internal/store"
	"github.com/Nils-witt/Tile-Server-Sync-GO/internal/tileserve"
)

// syncAll fetches and upserts geo objects for every map/version pair in maps
// once, recording each pair's outcome and the run as a whole in rec for the
// status web server to display, and returns the total number of objects
// synced across every pair that succeeded. maps is not necessarily every map
// in the current config — RunLoop passes only the maps currently due for a
// sync, per their own Interval, while the web UI's per-map
// "Sync" button passes a single map.
//
// A failure on one map/version (a fetch error or a store error) is logged
// and recorded against that pair, but does not stop the others from being
// attempted — one map being unreachable or misconfigured shouldn't prevent
// the rest of the fleet from syncing. If any pair failed, syncAll still
// returns a non-nil error (joining every failure) after all pairs have been
// attempted, so callers (RunLoop's logging, the web UI's per-map
// "Sync" button) can tell the run as a whole was not fully successful.
func syncAll(
	ctx context.Context, maps []config.MapTarget, clients map[string]*tileserve.Client, db *store.Store,
	rec *status.Recorder,
) (totalSynced int, err error) {
	defer func() { rec.RecordRun(totalSynced, err) }()

	var errs []error

	for _, m := range maps {
		client := clients[m.APIID]

		for _, version := range m.Versions {
			log.Printf("fetching geo objects for map %s version %s from api %s", m.ID, version, m.APIID)

			if client == nil {
				pairErr := fmt.Errorf("map %s version %s: api %q is not configured", m.ID, version, m.APIID)
				log.Printf("sync error: %v", pairErr)
				rec.RecordMapVersion(m.ID, version, 0, pairErr)
				errs = append(errs, pairErr)

				continue
			}

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
