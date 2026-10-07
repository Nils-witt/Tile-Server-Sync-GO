package webserver

import (
	"errors"
	"net/http"

	"github.com/Nils-witt/Tile-Server-Sync-GO/internal/config"
	"github.com/Nils-witt/Tile-Server-Sync-GO/internal/runner"
)

const maxMapBodyBytes = 1 << 16

// listMapsAPIHandler serves GET /api/maps: every configured map, in
// configured order. Requires view_config (enforced at the route level in
// webserver.go).
func listMapsAPIHandler(run Runner) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, run.Maps())
	}
}

// remoteMapDTO is one entry of GET /api/apis/{id}/remote-maps: a map tileserve-go
// offers, and whether it is already configured here.
type remoteMapDTO struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	Description    string `json:"description"`
	CurrentVersion string `json:"currentVersion"`
	Configured     bool   `json:"configured"`
}

// remoteMapsAPIHandler serves GET /api/apis/{id}/remote-maps: the maps the
// configured tileserve-go API {id} exposes (see runner.Runner.RemoteMaps),
// for the Maps tab's one-click add. An unknown API is a 404, an invalid one
// a 400, an unreachable or failing API a 502. Requires edit_config_maps.
func remoteMapsAPIHandler(run Runner) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		remote, err := run.RemoteMaps(r.Context(), r.PathValue("id"))
		if err != nil {
			status := http.StatusBadGateway
			if errors.Is(err, runner.ErrInvalid) {
				status = http.StatusBadRequest
			} else if errors.Is(err, runner.ErrAPINotFound) {
				status = http.StatusNotFound
			}

			writeJSON(w, status, map[string]string{"error": err.Error()})

			return
		}

		configured := make(map[string]bool)
		for _, m := range run.Maps() {
			configured[m.ID] = true
		}

		out := make([]remoteMapDTO, 0, len(remote))
		for _, m := range remote {
			out = append(out, remoteMapDTO{
				ID: m.UUID, Name: m.Name, Description: m.Description,
				CurrentVersion: m.CurrentVersion, Configured: configured[m.UUID],
			})
		}

		writeJSON(w, http.StatusOK, out)
	}
}

// mapSaveResponse is what POST /api/maps and PUT /api/maps/{id} return: the
// saved map plus whether the change was also applied live (see
// runner.ChangeResult).
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

// mapErrorStatus maps a Runner map error to the HTTP status it should be
// reported as.
func mapErrorStatus(err error) int {
	switch {
	case errors.Is(err, runner.ErrInvalid):
		return http.StatusBadRequest
	case errors.Is(err, runner.ErrMapNotFound):
		return http.StatusNotFound
	case errors.Is(err, runner.ErrMapIDTaken):
		return http.StatusConflict
	default:
		return http.StatusInternalServerError
	}
}

// createMapAPIHandler serves POST /api/maps. Requires edit_config_maps.
func createMapAPIHandler(run Runner) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var m config.MapTarget
		if err := decodeBody(w, r, maxMapBodyBytes, &m); err != nil {
			writeJSON(w, http.StatusBadRequest, mapSaveResponse{Error: err.Error()})
			return
		}

		created, res, err := run.CreateMap(r.Context(), actorOf(r), m)
		writeMapSave(w, created, res, err)
	}
}

// updateMapAPIHandler serves PUT /api/maps/{id}. The URL's {id} is
// authoritative — any id in the request body is ignored, so this can never
// rename a map. Requires edit_config_maps.
func updateMapAPIHandler(run Runner) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var m config.MapTarget
		if err := decodeBody(w, r, maxMapBodyBytes, &m); err != nil {
			writeJSON(w, http.StatusBadRequest, mapSaveResponse{Error: err.Error()})
			return
		}

		updated, res, err := run.UpdateMap(r.Context(), actorOf(r), r.PathValue("id"), m)
		writeMapSave(w, updated, res, err)
	}
}

func writeMapSave(w http.ResponseWriter, m config.MapTarget, res runner.ChangeResult, err error) {
	if err != nil {
		writeJSON(w, mapErrorStatus(err), mapSaveResponse{Error: err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, mapSaveResponse{
		Map: &m, Applied: res.ApplyErr == nil,
		ApplyError: errorString(res.ApplyErr), OverlayError: errorString(res.OverlayErr),
	})
}

// deleteMapAPIHandler serves DELETE /api/maps/{id}: removes the map from the
// configuration and purges its synced rows (see runner.Runner.DeleteMap).
// Requires edit_config_maps.
func deleteMapAPIHandler(run Runner) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		res, err := run.DeleteMap(r.Context(), actorOf(r), r.PathValue("id"))
		if err != nil {
			writeJSON(w, mapErrorStatus(err), mapDeleteResponse{Error: err.Error()})
			return
		}

		writeJSON(w, http.StatusOK, mapDeleteResponse{
			OK: true, Applied: res.ApplyErr == nil, ApplyError: errorString(res.ApplyErr),
			ObjectsDeleted: res.ObjectsDeleted, ObjectsError: errorString(res.ObjectsErr),
			OverlayError: errorString(res.OverlayErr),
		})
	}
}

// syncResponse is what POST /api/maps/{id}/sync returns.
type syncResponse struct {
	OK     bool   `json:"ok"`
	Synced int    `json:"synced"`
	Error  string `json:"error,omitempty"`
}

// syncMapAPIHandler runs the map named by the URL's {id} immediately (see
// runner.Runner.SyncMap — this works even for a disabled map) and reports
// how many objects were synced or why it failed. It blocks for as long as
// the sync takes. This is what the status page's per-map "Sync" button
// calls. Requires trigger_sync.
func syncMapAPIHandler(run Runner) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if id == "" {
			http.Error(w, "missing map id", http.StatusBadRequest)
			return
		}

		synced, err := run.SyncMap(r.Context(), id)
		if err != nil {
			writeJSON(w, http.StatusBadGateway, syncResponse{Synced: synced, Error: err.Error()})
			return
		}

		writeJSON(w, http.StatusOK, syncResponse{OK: true, Synced: synced})
	}
}
