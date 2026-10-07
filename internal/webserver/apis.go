package webserver

import (
	"errors"
	"net/http"

	"github.com/Nils-witt/Tile-Server-Sync-GO/internal/config"
	"github.com/Nils-witt/Tile-Server-Sync-GO/internal/runner"
)

const maxAPIBodyBytes = 1 << 16

// listAPIsHandler serves GET /api/apis: every configured API, in
// configured order, passwords blanked. Requires view_config.
func listAPIsHandler(run Runner) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, run.APIs())
	}
}

// apiSaveResponse is what POST /api/apis and PUT /api/apis/{id} return: the
// saved API (password blanked) plus whether the change was also applied
// live (see runner.ChangeResult).
type apiSaveResponse struct {
	API          *config.API `json:"api,omitempty"`
	Error        string      `json:"error,omitempty"`
	Applied      bool        `json:"applied,omitempty"`
	ApplyError   string      `json:"applyError,omitempty"`
	OverlayError string      `json:"overlayError,omitempty"`
}

// apiDeleteResponse is what DELETE /api/apis/{id} returns.
type apiDeleteResponse struct {
	OK         bool   `json:"ok"`
	Error      string `json:"error,omitempty"`
	Applied    bool   `json:"applied,omitempty"`
	ApplyError string `json:"applyError,omitempty"`
}

// apiErrorStatus maps a Runner API error to the HTTP status it should be
// reported as. A failed pre-save connection test is a 422.
func apiErrorStatus(err error) int {
	switch {
	case errors.Is(err, runner.ErrInvalid):
		return http.StatusBadRequest
	case errors.Is(err, runner.ErrTestFailed):
		return http.StatusUnprocessableEntity
	case errors.Is(err, runner.ErrAPINotFound):
		return http.StatusNotFound
	case errors.Is(err, runner.ErrAPIIDTaken), errors.Is(err, runner.ErrAPIInUse):
		return http.StatusConflict
	default:
		return http.StatusInternalServerError
	}
}

// createAPIHandler serves POST /api/apis: tests the API and only saves it
// if the test passes. Requires edit_config_api.
func createAPIHandler(run Runner) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var a config.API
		if err := decodeBody(w, r, maxAPIBodyBytes, &a); err != nil {
			writeJSON(w, http.StatusBadRequest, apiSaveResponse{Error: err.Error()})
			return
		}

		created, res, err := run.CreateAPI(r.Context(), actorOf(r), a)
		writeAPISave(w, created, res, err)
	}
}

// updateAPIHandler serves PUT /api/apis/{id}. The URL's {id} is
// authoritative; a blank password keeps the stored one. Requires
// edit_config_api.
func updateAPIHandler(run Runner) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var a config.API
		if err := decodeBody(w, r, maxAPIBodyBytes, &a); err != nil {
			writeJSON(w, http.StatusBadRequest, apiSaveResponse{Error: err.Error()})
			return
		}

		updated, res, err := run.UpdateAPI(r.Context(), actorOf(r), r.PathValue("id"), a)
		writeAPISave(w, updated, res, err)
	}
}

func writeAPISave(w http.ResponseWriter, a config.API, res runner.ChangeResult, err error) {
	if err != nil {
		writeJSON(w, apiErrorStatus(err), apiSaveResponse{Error: err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, apiSaveResponse{
		API: &a, Applied: res.ApplyErr == nil,
		ApplyError: errorString(res.ApplyErr), OverlayError: errorString(res.OverlayErr),
	})
}

// deleteAPIHandler serves DELETE /api/apis/{id}; an API still used by a
// map is a 409. Requires edit_config_api.
func deleteAPIHandler(run Runner) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		res, err := run.DeleteAPI(r.Context(), actorOf(r), r.PathValue("id"))
		if err != nil {
			writeJSON(w, apiErrorStatus(err), apiDeleteResponse{Error: err.Error()})
			return
		}

		writeJSON(w, http.StatusOK, apiDeleteResponse{
			OK: true, Applied: res.ApplyErr == nil, ApplyError: errorString(res.ApplyErr),
		})
	}
}
