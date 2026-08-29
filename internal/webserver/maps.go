package webserver

import (
	"Tile-Server-Sync-GO/internal/config"
	"Tile-Server-Sync-GO/internal/configdb"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

const maxMapBodyBytes = 1 << 16

// listMapsAPIHandler serves GET /api/maps: every configured map, in
// configured order. Requires view_config (enforced at the route level in
// webserver.go).
func listMapsAPIHandler(cfgDB *configdb.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		maps, err := cfgDB.ListMaps(r.Context())
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, errorJSON(err.Error()))
			return
		}

		writeJSON(w, http.StatusOK, maps)
	}
}

// getMapAPIHandler serves GET /api/maps/{id}: one configured map. Requires
// view_config.
func getMapAPIHandler(cfgDB *configdb.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		m, err := cfgDB.GetMap(r.Context(), r.PathValue("id"))
		if err != nil {
			writeJSON(w, mapErrorStatus(err), errorJSON(err.Error()))
			return
		}

		writeJSON(w, http.StatusOK, m)
	}
}

// mapSaveResponse is what POST /api/maps and PUT /api/maps/{id} return: the
// saved map plus whether the change was also applied live — the same
// Applied/ApplyError concept finishConfigSave uses for a whole config save,
// scoped to just this one map.
type mapSaveResponse struct {
	Map          *config.MapTarget `json:"map,omitempty"`
	Error        string            `json:"error,omitempty"`
	Applied      bool              `json:"applied,omitempty"`
	ApplyError   string            `json:"applyError,omitempty"`
	OverlayError string            `json:"overlayError,omitempty"`
}

// mapDeleteResponse is what DELETE /api/maps/{id} returns.
type mapDeleteResponse struct {
	OK             bool   `json:"ok"`
	Error          string `json:"error,omitempty"`
	Applied        bool   `json:"applied,omitempty"`
	ApplyError     string `json:"applyError,omitempty"`
	ObjectsDeleted int64  `json:"objectsDeleted,omitempty"`
	ObjectsError   string `json:"objectsError,omitempty"`
	OverlayError   string `json:"overlayError,omitempty"`
}

// mapErrorStatus maps a configdb map-lookup error to the HTTP status it
// should be reported as: 404 for ErrMapNotFound, 409 for ErrMapIDTaken, 500
// for anything else (an unexpected database failure).
func mapErrorStatus(err error) int {
	switch {
	case errors.Is(err, configdb.ErrMapNotFound):
		return http.StatusNotFound
	case errors.Is(err, configdb.ErrMapIDTaken):
		return http.StatusConflict
	default:
		return http.StatusInternalServerError
	}
}

func decodeMapBody(w http.ResponseWriter, r *http.Request) (config.MapTarget, error) {
	var m config.MapTarget
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxMapBodyBytes)).Decode(&m); err != nil {
		return config.MapTarget{}, fmt.Errorf("decode request: %w", err)
	}

	return m, nil
}

// validateCandidateMap validates m against the currently stored config's
// other maps/database columns via config.Config.ValidateMaps, which needs
// the *real* full maps list (not just m in isolation) to catch a duplicate
// id or a staticColumns collision with database.columns. replaceID, if
// non-empty, is the id of an existing map m is replacing (for an update);
// left empty for a create, where m is simply appended to the candidate list.
func validateCandidateMap(ctx context.Context, cfgDB *configdb.Store, m config.MapTarget, replaceID string) error {
	stored, err := cfgDB.Load(ctx)
	if err != nil {
		return fmt.Errorf("load stored config: %w", err)
	}

	candidate := make([]config.MapTarget, 0, len(stored.Maps)+1)
	replaced := false

	for _, existing := range stored.Maps {
		if replaceID != "" && existing.ID == replaceID {
			candidate = append(candidate, m)
			replaced = true

			continue
		}

		candidate = append(candidate, existing)
	}

	if !replaced {
		candidate = append(candidate, m)
	}

	scratch := &config.Config{Database: stored.Database, Maps: candidate}

	return scratch.ValidateMaps()
}

// createMapAPIHandler serves POST /api/maps. Requires edit_config_maps.
// createMapOverlays keeps the EDP map_src_overlays table in sync with the
// new map (a no-op if Database.SyncOverlays isn't enabled — see
// internal/store's CreateMapOverlays); a failure there is reported via the
// response's OverlayError but doesn't fail the request, since the map itself
// was already successfully created.
func createMapAPIHandler(
	cfgDB *configdb.Store, reload func(context.Context) error,
	createMapOverlays func(context.Context, config.MapTarget) error,
) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		m, err := decodeMapBody(w, r)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, mapSaveResponse{Error: err.Error()})
			return
		}

		if err := validateCandidateMap(r.Context(), cfgDB, m, ""); err != nil {
			writeJSON(w, http.StatusBadRequest, mapSaveResponse{Error: err.Error()})
			return
		}

		created, err := cfgDB.CreateMap(r.Context(), m)
		if err != nil {
			writeJSON(w, mapErrorStatus(err), mapSaveResponse{Error: err.Error()})
			return
		}

		if actor, ok := currentUser(r.Context()); ok {
			logSecurityEvent(r, cfgDB, "map_created", actor.Username, fmt.Sprintf("map %q created", created.ID))
		}

		var overlayErr string
		if err := createMapOverlays(r.Context(), *created); err != nil {
			overlayErr = err.Error()
		}

		finishMapChange(w, r, reload, created, overlayErr)
	}
}

// updateMapAPIHandler serves PUT /api/maps/{id}: replaces the map's
// versions/interval/staticColumns. The URL's {id} is authoritative — any id
// in the request body is discarded in favor of it, so this can never rename
// a map. Requires edit_config_maps. updateMapOverlays keeps map_src_overlays
// in sync with the edit the same way createMapAPIHandler's createMapOverlays
// does.
func updateMapAPIHandler(
	cfgDB *configdb.Store, reload func(context.Context) error,
	updateMapOverlays func(context.Context, config.MapTarget, config.MapTarget) error,
) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")

		m, err := decodeMapBody(w, r)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, mapSaveResponse{Error: err.Error()})
			return
		}

		m.ID = id

		before, err := cfgDB.GetMap(r.Context(), id)
		if err != nil {
			writeJSON(w, mapErrorStatus(err), mapSaveResponse{Error: err.Error()})
			return
		}

		if err := validateCandidateMap(r.Context(), cfgDB, m, id); err != nil {
			writeJSON(w, http.StatusBadRequest, mapSaveResponse{Error: err.Error()})
			return
		}

		updated, err := cfgDB.UpdateMap(r.Context(), id, m)
		if err != nil {
			writeJSON(w, mapErrorStatus(err), mapSaveResponse{Error: err.Error()})
			return
		}

		if actor, ok := currentUser(r.Context()); ok {
			detail := fmt.Sprintf("map %q: no changes", id)
			if changes := diffMapFields(*before, *updated); len(changes) > 0 {
				detail = fmt.Sprintf("map %q: %s", id, strings.Join(changes, ", "))
			}

			logSecurityEvent(r, cfgDB, "map_updated", actor.Username, detail)
		}

		var overlayErr string
		if err := updateMapOverlays(r.Context(), *before, *updated); err != nil {
			overlayErr = err.Error()
		}

		finishMapChange(w, r, reload, updated, overlayErr)
	}
}

// deleteMapAPIHandler serves DELETE /api/maps/{id}: removes the map from
// configuration and, via deleteMapObjects, purges every geo_objects row
// previously synced for it (across all versions) — otherwise those rows
// would linger in the database forever with nothing left to prune them.
// deleteMapOverlays likewise removes the map's map_src_overlays rows (a
// no-op if Database.SyncOverlays isn't enabled); it's called with the map as
// it was before deletion (fetched up front, since it's gone from configdb
// once cfgDB.DeleteMap succeeds) so it still knows the map's versions.
// Requires edit_config_maps.
func deleteMapAPIHandler(
	cfgDB *configdb.Store, reload func(context.Context) error,
	deleteMapObjects func(context.Context, string) (int64, error),
	deleteMapOverlays func(context.Context, config.MapTarget) error,
) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")

		before, err := cfgDB.GetMap(r.Context(), id)
		if err != nil {
			writeJSON(w, mapErrorStatus(err), mapDeleteResponse{Error: err.Error()})
			return
		}

		if err := cfgDB.DeleteMap(r.Context(), id); err != nil {
			writeJSON(w, mapErrorStatus(err), mapDeleteResponse{Error: err.Error()})
			return
		}

		resp := mapDeleteResponse{OK: true}

		deleted, objErr := deleteMapObjects(r.Context(), id)
		resp.ObjectsDeleted = deleted

		detail := fmt.Sprintf("map %q deleted", id)

		if objErr != nil {
			resp.ObjectsError = objErr.Error()
			detail = fmt.Sprintf("%s (failed to delete synced objects: %v)", detail, objErr)
		} else if deleted > 0 {
			detail = fmt.Sprintf("%s (%d synced object(s) deleted)", detail, deleted)
		}

		if overlayErr := deleteMapOverlays(r.Context(), *before); overlayErr != nil {
			resp.OverlayError = overlayErr.Error()
		}

		if actor, ok := currentUser(r.Context()); ok {
			logSecurityEvent(r, cfgDB, "map_deleted", actor.Username, detail)
		}

		if applyErr := reload(r.Context()); applyErr != nil {
			resp.ApplyError = applyErr.Error()
		} else {
			resp.Applied = true
		}

		writeJSON(w, http.StatusOK, resp)
	}
}

// finishMapChange is the create/update tail shared by createMapAPIHandler
// and updateMapAPIHandler: apply the change live via reload (the same
// mechanism finishConfigSave uses in config.go) and report whether that
// succeeded, alongside the saved map and overlayErr (already stringified by
// the caller, since createMapOverlays/updateMapOverlays's errors.Join result
// isn't itself worth wrapping further here).
func finishMapChange(
	w http.ResponseWriter, r *http.Request, reload func(context.Context) error, m *config.MapTarget, overlayErr string,
) {
	resp := mapSaveResponse{Map: m, OverlayError: overlayErr}

	if applyErr := reload(r.Context()); applyErr != nil {
		resp.ApplyError = applyErr.Error()
	} else {
		resp.Applied = true
	}

	writeJSON(w, http.StatusOK, resp)
}

// syncResponse is what POST /api/maps/{id}/sync returns.
type syncResponse struct {
	OK     bool   `json:"ok"`
	Synced int    `json:"synced"`
	Error  string `json:"error,omitempty"`
}

// syncMapAPIHandler calls syncMap with the map ID taken from the URL's {id}
// path value on each POST, running that one map's sync immediately
// (serialized against any concurrent scheduled sync — see
// runtime.runSyncMaps in main.go) instead of waiting for its next interval
// tick, and reports how many objects were synced or why it failed. It blocks
// for as long as the sync takes. This is what the status page's per-map
// "Sync" button calls. Requires trigger_sync.
func syncMapAPIHandler(syncMap func(context.Context, string) (int, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if id == "" {
			http.Error(w, "missing map id", http.StatusBadRequest)
			return
		}

		synced, err := syncMap(r.Context(), id)
		if err != nil {
			writeJSON(w, http.StatusBadGateway, syncResponse{Synced: synced, Error: err.Error()})
			return
		}

		writeJSON(w, http.StatusOK, syncResponse{OK: true, Synced: synced})
	}
}
