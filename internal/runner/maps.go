package runner

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/Nils-witt/Tile-Server-Sync-GO/internal/config"
)

// Maps returns every stored map, in configured order.
func (r *Runner) Maps() []config.MapTarget {
	return r.storedCopy().Maps
}

// CreateMap validates m against the other stored maps, persists it, keeps
// EDP's map_src_overlays in sync (a no-op unless Database.SyncOverlays is
// enabled; a failure there is only reported via ChangeResult.OverlayErr,
// since the map itself was created), and applies the result live. A
// validation failure wraps ErrInvalid; a duplicate id returns ErrMapIDTaken.
func (r *Runner) CreateMap(
	ctx context.Context, actor Actor, m config.MapTarget,
) (config.MapTarget, ChangeResult, error) {
	r.writeMu.Lock()
	defer r.writeMu.Unlock()

	cfg := r.storedCopy()
	cfg.Maps = append(cfg.Maps, cloneMap(m))

	if err := validateMaps(cfg); err != nil {
		return config.MapTarget{}, ChangeResult{}, err
	}

	created, err := r.cfgDB.CreateMap(ctx, m)
	if err != nil {
		return config.MapTarget{}, ChangeResult{}, err
	}

	cfg.Maps[len(cfg.Maps)-1] = cloneMap(*created)
	r.setStored(cfg)
	r.LogSecurityEvent(ctx, actor, "map_created", fmt.Sprintf("map %q created", created.ID))

	res := ChangeResult{OverlayErr: r.createMapOverlays(ctx, *created)}
	res.ApplyErr = r.apply(ctx)

	return *created, res, nil
}

// UpdateMap replaces the map identified by id with m; id is authoritative
// (m.ID is ignored), so a map can never be renamed. Otherwise it behaves
// like CreateMap, returning ErrMapNotFound for an unknown id.
func (r *Runner) UpdateMap(
	ctx context.Context, actor Actor, id string, m config.MapTarget,
) (config.MapTarget, ChangeResult, error) {
	r.writeMu.Lock()
	defer r.writeMu.Unlock()

	m.ID = id
	cfg := r.storedCopy()

	idx := slices.IndexFunc(cfg.Maps, func(existing config.MapTarget) bool { return existing.ID == id })
	if idx < 0 {
		return config.MapTarget{}, ChangeResult{}, fmt.Errorf("update map %q: %w", id, ErrMapNotFound)
	}

	before := cfg.Maps[idx]
	cfg.Maps[idx] = cloneMap(m)

	if err := validateMaps(cfg); err != nil {
		return config.MapTarget{}, ChangeResult{}, err
	}

	updated, err := r.cfgDB.UpdateMap(ctx, id, m)
	if err != nil {
		return config.MapTarget{}, ChangeResult{}, err
	}

	cfg.Maps[idx] = cloneMap(*updated)
	r.setStored(cfg)

	detail := fmt.Sprintf("map %q: no changes", id)
	if changes := diffMapFields(before, *updated); len(changes) > 0 {
		detail = fmt.Sprintf("map %q: %s", id, strings.Join(changes, ", "))
	}

	r.LogSecurityEvent(ctx, actor, "map_updated", detail)

	res := ChangeResult{OverlayErr: r.updateMapOverlays(ctx, before, *updated)}
	res.ApplyErr = r.apply(ctx)

	return *updated, res, nil
}

// DeleteMap removes the map identified by id from the config, purges every
// geo_objects row previously synced for it (across all versions — nothing
// would ever prune them otherwise) and its map_src_overlays rows, and
// applies the result live. Returns ErrMapNotFound for an unknown id.
func (r *Runner) DeleteMap(ctx context.Context, actor Actor, id string) (ChangeResult, error) {
	r.writeMu.Lock()
	defer r.writeMu.Unlock()

	cfg := r.storedCopy()

	idx := slices.IndexFunc(cfg.Maps, func(existing config.MapTarget) bool { return existing.ID == id })
	if idx < 0 {
		return ChangeResult{}, fmt.Errorf("delete map %q: %w", id, ErrMapNotFound)
	}

	before := cfg.Maps[idx]

	if err := r.cfgDB.DeleteMap(ctx, id); err != nil {
		return ChangeResult{}, err
	}

	cfg.Maps = slices.Delete(cfg.Maps, idx, idx+1)
	r.setStored(cfg)

	var res ChangeResult

	res.ObjectsDeleted, res.ObjectsErr = r.deleteMapObjects(ctx, id)

	detail := fmt.Sprintf("map %q deleted", id)
	if res.ObjectsErr != nil {
		detail = fmt.Sprintf("%s (failed to delete synced objects: %v)", detail, res.ObjectsErr)
	} else if res.ObjectsDeleted > 0 {
		detail = fmt.Sprintf("%s (%d synced object(s) deleted)", detail, res.ObjectsDeleted)
	}

	res.OverlayErr = r.deleteMapOverlays(ctx, before)
	r.LogSecurityEvent(ctx, actor, "map_deleted", detail)
	res.ApplyErr = r.apply(ctx)

	return res, nil
}

// validateMaps runs config.ValidateMaps on cfg (the stored config with a
// candidate change already merged in), which needs the full maps list to
// catch a duplicate id or a staticColumns collision with database.columns.
func validateMaps(cfg *config.Config) error {
	if err := cfg.ValidateMaps(); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalid, err)
	}

	return nil
}

// SyncMap syncs the single map mapID immediately, regardless of its own
// schedule or Disabled flag: Disabled only opts a map out of automatic
// scheduling, not an explicit request like the status page's "Sync"
// button. It blocks for as long as the sync takes.
func (r *Runner) SyncMap(ctx context.Context, mapID string) (int, error) {
	return r.runSyncMaps(ctx, map[string]struct{}{mapID: {}})
}

// runSyncMaps runs syncAll against just the active config's maps whose ID is
// in ids, serialized via syncMu. It re-reads the active state itself (rather
// than trusting maps handed to it earlier) so it always syncs each map's
// latest versions/staticColumns, even if an apply landed between Run
// computing ids and this call acquiring syncMu.
func (r *Runner) runSyncMaps(ctx context.Context, ids map[string]struct{}) (int, error) {
	r.syncMu.Lock()
	defer r.syncMu.Unlock()

	cfg, client, db := r.active()
	if db == nil {
		r.rec.RecordRun(0, ErrNotConfigured)
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

	return syncAll(ctx, due, client, db, r.rec)
}

// deleteMapObjects deletes every previously synced geo_objects row for
// mapID, serialized against syncs via syncMu (a concurrent sync of that map
// could otherwise re-insert rows mid-delete). A no-op before the first
// successful apply — there's nothing to clean up then.
//
// It also strips mapID from the active config, still under syncMu: the
// apply that follows a delete only succeeds if the whole remaining config is
// valid and reachable, and if it fails, the stale active config would keep
// re-syncing the deleted map and re-inserting the rows just purged. It also
// drops the map's results from the status page.
func (r *Runner) deleteMapObjects(ctx context.Context, mapID string) (int64, error) {
	r.syncMu.Lock()
	defer r.syncMu.Unlock()

	r.removeActiveMap(mapID)
	r.rec.RemoveMap(mapID)

	_, _, db := r.active()
	if db == nil {
		return 0, nil
	}

	return db.DeleteMapObjects(ctx, mapID)
}

// removeActiveMap removes mapID from the active config by swapping in a new
// *config.Config with a filtered Maps slice rather than mutating it in place,
// since active() callers may be reading the old slice without r.mu held.
func (r *Runner) removeActiveMap(mapID string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.cfg == nil {
		return
	}

	kept := slices.DeleteFunc(slices.Clone(r.cfg.Maps), func(m config.MapTarget) bool { return m.ID == mapID })
	if len(kept) == len(r.cfg.Maps) {
		return
	}

	newCfg := *r.cfg
	newCfg.Maps = kept
	r.cfg = &newCfg
}

// createMapOverlays, updateMapOverlays and deleteMapOverlays keep the EDP
// map_src_overlays table (see internal/store's overlays.go) in sync with a
// map change, serialized against syncs via syncMu. Each is a no-op before
// the first successful apply.
func (r *Runner) createMapOverlays(ctx context.Context, m config.MapTarget) error {
	r.syncMu.Lock()
	defer r.syncMu.Unlock()

	cfg, _, db := r.active()
	if db == nil {
		return nil
	}

	return db.CreateMapOverlays(ctx, cfg.API.BaseURL, m)
}

func (r *Runner) updateMapOverlays(ctx context.Context, before, after config.MapTarget) error {
	r.syncMu.Lock()
	defer r.syncMu.Unlock()

	cfg, _, db := r.active()
	if db == nil {
		return nil
	}

	return db.UpdateMapOverlays(ctx, cfg.API.BaseURL, before, after)
}

func (r *Runner) deleteMapOverlays(ctx context.Context, m config.MapTarget) error {
	r.syncMu.Lock()
	defer r.syncMu.Unlock()

	cfg, _, db := r.active()
	if db == nil {
		return nil
	}

	return db.DeleteMapOverlays(ctx, cfg.API.BaseURL, m)
}
