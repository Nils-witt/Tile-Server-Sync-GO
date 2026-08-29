package store

import (
	"Tile-Server-Sync-GO/internal/config"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"regexp"
	"strings"
)

// map_src_overlays is an external application's (EDP) overlay table this
// tool keeps in sync when Database.SyncOverlays is enabled. Unlike the
// synced geo_objects table, EnsureSchema never creates it: its schema
// (LIZENZ, KONFIG, OFFLINE_CACHE_* columns, ...) belongs entirely to that
// other application, so a deployment enabling SyncOverlays is expected to
// already have it. Every row this package writes sets every column other
// than NAME/SOURCE/CACHE_LOKAL to a fixed value matching a manually created
// OSM overlay row for this deployment — TYP, ENABLED_MAP/ENABLED_WEB,
// OPACITY, RELOAD, and the OFFLINE_CACHE_*/CACHE_SERVER/CACHE_FILE columns
// are not currently configurable.

// overlayNonAlnum matches runs of characters that aren't safe to carry
// verbatim into an EDP overlay row's CACHE_LOKAL folder name.
var overlayNonAlnum = regexp.MustCompile(`[^a-zA-Z0-9]+`)

// overlaySlug lowercases s and collapses every run of non-alphanumeric
// characters into a single underscore, trimming any leading/trailing
// underscore left behind — e.g. "2026_PueMa_Plan " -> "2026_puema_plan".
func overlaySlug(s string) string {
	slug := overlayNonAlnum.ReplaceAllString(strings.ToLower(strings.TrimSpace(s)), "_")

	return strings.Trim(slug, "_")
}

// overlaySource builds the SOURCE URL an EDP overlay row for mapID/version
// points at, rooted at apiBaseURL (config.API.BaseURL) — the same
// tileserve-go instance this tool already talks to. Since it embeds both
// mapID and version, it also doubles as the natural per-map/version key used
// to find an existing row to update or delete (map_src_overlays has no
// column of its own referencing back to this tool's map/version identity).
func overlaySource(apiBaseURL, mapID, version string) string {
	return fmt.Sprintf("%s/maps/%s/version/%s/", strings.TrimRight(apiBaseURL, "/"), mapID, version)
}

// overlayCacheLocal builds an EDP overlay row's CACHE_LOKAL folder name from
// a map's Name, disambiguated by version when the map has more than one
// configured version (multiVersion), so two versions of the same map don't
// collide on the same local cache folder.
func overlayCacheLocal(name, version string, multiVersion bool) string {
	slug := overlaySlug(name)
	if multiVersion {
		slug += "_" + overlaySlug(version)
	}

	return slug + `\`
}

// upsertOverlayRow writes one EDP overlay row identified by source: updating
// NAME/CACHE_LOKAL if a row with that SOURCE already exists, inserting a new
// row (letting its ID auto-increment) otherwise.
func (s *Store) upsertOverlayRow(ctx context.Context, source, name, cacheLocal string) error {
	var rowID int64

	err := s.db.QueryRowContext(ctx,
		`SELECT ID FROM map_src_overlays WHERE SOURCE = ?`, source).Scan(&rowID)

	switch {
	case errors.Is(err, sql.ErrNoRows):
		if _, err := s.db.ExecContext(ctx,
			`INSERT INTO map_src_overlays
				(NAME, TYP, ENABLED_MAP, ENABLED_WEB, SOURCE, LIZENZ, LIZENZ_URL, OPACITY, RELOAD, KONFIG,
				 OFFLINE_CACHE_LOKAL, CACHE_LOKAL, OFFLINE_CACHE_SERVER, CACHE_SERVER, CACHE_FILE)
				VALUES (?, 'OSM', 1, 1, ?, '', NULL, 255, -1, '', 0, ?, 0, NULL, NULL)`,
			name, source, cacheLocal); err != nil {
			return fmt.Errorf("insert overlay row for %q: %w", source, err)
		}

		return nil
	case err != nil:
		return fmt.Errorf("look up overlay row for %q: %w", source, err)
	}

	if _, err := s.db.ExecContext(ctx,
		`UPDATE map_src_overlays SET NAME = ?, CACHE_LOKAL = ? WHERE ID = ?`,
		name, cacheLocal, rowID); err != nil {
		return fmt.Errorf("update overlay row for %q: %w", source, err)
	}

	return nil
}

// deleteOverlayRow removes the EDP overlay row identified by source, if any.
func (s *Store) deleteOverlayRow(ctx context.Context, source string) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM map_src_overlays WHERE SOURCE = ?`, source); err != nil {
		return fmt.Errorf("delete overlay row for %q: %w", source, err)
	}

	return nil
}

// CreateMapOverlays inserts (or, if SOURCE already matches an existing row,
// updates) one map_src_overlays row per version in m.Versions, so a newly
// created map immediately shows up as a selectable overlay in EDP. A no-op
// if SyncOverlays isn't enabled. A map with no Name configured is skipped
// (logged, not an error) since EDP overlay rows need a human-readable NAME.
func (s *Store) CreateMapOverlays(ctx context.Context, apiBaseURL string, m config.MapTarget) error {
	if !s.syncOverlays {
		return nil
	}

	if m.Name == "" {
		log.Printf("map %s has no name configured; skipping EDP overlay sync", m.ID)
		return nil
	}

	multi := len(m.Versions) > 1

	var errs []error

	for _, version := range m.Versions {
		source := overlaySource(apiBaseURL, m.ID, version)
		if err := s.upsertOverlayRow(ctx, source, m.Name, overlayCacheLocal(m.Name, version, multi)); err != nil {
			errs = append(errs, err)
		}
	}

	return errors.Join(errs...)
}

// UpdateMapOverlays reconciles a map's map_src_overlays rows after an edit:
// a version present in before.Versions but no longer in after.Versions has
// its row deleted; every version in after.Versions is upserted (inserted if
// missing, or updated in place if a matching row already exists) with
// after's current Name. A no-op if SyncOverlays isn't enabled. If after has
// no Name, versions dropped by the edit are still cleaned up, but no row is
// (re)written for the remaining ones — matching CreateMapOverlays' skip.
func (s *Store) UpdateMapOverlays(ctx context.Context, apiBaseURL string, before, after config.MapTarget) error {
	if !s.syncOverlays {
		return nil
	}

	afterVersions := make(map[string]bool, len(after.Versions))
	for _, v := range after.Versions {
		afterVersions[v] = true
	}

	var errs []error

	for _, v := range before.Versions {
		if afterVersions[v] {
			continue
		}

		if err := s.deleteOverlayRow(ctx, overlaySource(apiBaseURL, before.ID, v)); err != nil {
			errs = append(errs, err)
		}
	}

	if after.Name == "" {
		if len(after.Versions) > 0 {
			log.Printf("map %s has no name configured; skipping EDP overlay sync", after.ID)
		}

		return errors.Join(errs...)
	}

	multi := len(after.Versions) > 1

	for _, version := range after.Versions {
		source := overlaySource(apiBaseURL, after.ID, version)
		if err := s.upsertOverlayRow(ctx, source, after.Name, overlayCacheLocal(after.Name, version, multi)); err != nil {
			errs = append(errs, err)
		}
	}

	return errors.Join(errs...)
}

// DeleteMapOverlays removes every map_src_overlays row for m (one per
// configured version), called when a map is removed from configuration
// entirely. A no-op if SyncOverlays isn't enabled.
func (s *Store) DeleteMapOverlays(ctx context.Context, apiBaseURL string, m config.MapTarget) error {
	if !s.syncOverlays {
		return nil
	}

	var errs []error

	for _, version := range m.Versions {
		if err := s.deleteOverlayRow(ctx, overlaySource(apiBaseURL, m.ID, version)); err != nil {
			errs = append(errs, err)
		}
	}

	return errors.Join(errs...)
}
