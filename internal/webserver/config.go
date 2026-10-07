package webserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/Nils-witt/Tile-Server-Sync-GO/internal/config"
	"github.com/Nils-witt/Tile-Server-Sync-GO/internal/runner"
)

// configGetResponse is what each of the section save endpoints below
// returns. Cfg is the saved config, with its secrets redacted by the Runner
// (stored passwords never round-trip into the config page's form fields; a
// blank one in a save request means "unchanged"). Applied reports whether
// the save was also applied to the running process; ApplyError carries why
// not, if the save itself succeeded but applying it failed (an incomplete
// config during initial setup, or an unreachable API/database) — the save
// is not rolled back in that case.
type configGetResponse struct {
	Cfg        *config.Config `json:"config,omitempty"`
	Error      string         `json:"error,omitempty"`
	Applied    bool           `json:"applied,omitempty"`
	ApplyError string         `json:"applyError,omitempty"`
}

const maxConfigBodyBytes = 1 << 20 // 1 MiB; config is never remotely this large

// apiSectionRequest/databaseSectionRequest are the request/response bodies
// for the API/Database section endpoints — each submits or returns only its
// own tab's fields.
type apiSectionRequest struct {
	API config.API `json:"api"`
}

type databaseSectionRequest struct {
	Database config.Database `json:"database"`
}

// getAPISectionHandler serves GET /api/config/api. Requires view_config.
func getAPISectionHandler(run Runner) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, apiSectionRequest{API: run.Config().API})
	}
}

// getDatabaseSectionHandler serves GET /api/config/database. Requires
// view_config.
func getDatabaseSectionHandler(run Runner) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, databaseSectionRequest{Database: run.Config().Database})
	}
}

// saveAPISectionHandler serves PUT /api/config/api. Requires
// edit_config_api (enforced at the route level).
func saveAPISectionHandler(run Runner) http.HandlerFunc {
	return sectionSaveHandler(func(ctx context.Context, actor runner.Actor, req apiSectionRequest) (
		config.Config, runner.ChangeResult, error,
	) {
		return run.SaveAPI(ctx, actor, req.API)
	})
}

// saveDatabaseSectionHandler serves PUT /api/config/database. Requires
// edit_config_database.
func saveDatabaseSectionHandler(run Runner) http.HandlerFunc {
	return sectionSaveHandler(func(ctx context.Context, actor runner.Actor, req databaseSectionRequest) (
		config.Config, runner.ChangeResult, error,
	) {
		return run.SaveDatabase(ctx, actor, req.Database)
	})
}

// sectionTestResponse is the body of POST /api/config/{api,database}/test:
// OK, or why the test failed.
type sectionTestResponse struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

// testAPIHandler serves POST /api/config/api/test: it tests the submitted
// (unsaved) API section. Requires edit_config_api.
func testAPIHandler(run Runner) http.HandlerFunc {
	return sectionTestHandler(func(ctx context.Context, actor runner.Actor, req apiSectionRequest) error {
		return run.TestAPI(ctx, actor, req.API)
	})
}

// testDatabaseHandler serves POST /api/config/database/test: it tests the
// submitted (unsaved) database section. Requires edit_config_database.
func testDatabaseHandler(run Runner) http.HandlerFunc {
	return sectionTestHandler(func(ctx context.Context, actor runner.Actor, req databaseSectionRequest) error {
		return run.TestDatabase(ctx, actor, req.Database)
	})
}

// sectionTestHandler decodes the request body as a T and hands it to test.
// Invalid settings are a 400; a failed connection is still a 200 with
// ok=false, since the test itself ran fine.
func sectionTestHandler[T any](test func(ctx context.Context, actor runner.Actor, req T) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req T
		if err := decodeBody(w, r, maxConfigBodyBytes, &req); err != nil {
			writeJSON(w, http.StatusBadRequest, sectionTestResponse{Error: err.Error()})
			return
		}

		err := test(r.Context(), actorOf(r), req)
		if errors.Is(err, runner.ErrInvalid) {
			writeJSON(w, http.StatusBadRequest, sectionTestResponse{Error: err.Error()})
			return
		}

		writeJSON(w, http.StatusOK, sectionTestResponse{OK: err == nil, Error: errorString(err)})
	}
}

// sectionSaveHandler decodes the request body as a T and hands it to save,
// translating the outcome into a configGetResponse. A section that fails
// its pre-save test isn't saved: invalid settings are a 400, a failed
// connection a 422.
func sectionSaveHandler[T any](
	save func(ctx context.Context, actor runner.Actor, req T) (config.Config, runner.ChangeResult, error),
) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req T
		if err := decodeBody(w, r, maxConfigBodyBytes, &req); err != nil {
			writeJSON(w, http.StatusBadRequest, configGetResponse{Error: err.Error()})
			return
		}

		cfg, res, err := save(r.Context(), actorOf(r), req)

		switch {
		case errors.Is(err, runner.ErrInvalid):
			writeJSON(w, http.StatusBadRequest, configGetResponse{Error: err.Error()})
			return
		case errors.Is(err, runner.ErrTestFailed):
			writeJSON(w, http.StatusUnprocessableEntity, configGetResponse{Error: err.Error()})
			return
		case err != nil:
			writeJSON(w, http.StatusInternalServerError, configGetResponse{Error: err.Error()})
			return
		}

		resp := configGetResponse{Cfg: &cfg, Applied: res.ApplyErr == nil}
		if res.ApplyErr != nil {
			resp.ApplyError = res.ApplyErr.Error()
		}

		writeJSON(w, http.StatusOK, resp)
	}
}

func decodeBody(w http.ResponseWriter, r *http.Request, limit int64, v any) error {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, limit)).Decode(v); err != nil {
		return fmt.Errorf("decode request: %w", err)
	}

	return nil
}

// actorOf identifies the request's signed-in user for the security log.
func actorOf(r *http.Request) runner.Actor {
	actor := runner.Actor{RemoteAddr: r.RemoteAddr}
	if user, ok := currentUser(r.Context()); ok {
		actor.Username = user.Username
	}

	return actor
}

// errorString returns err's message, or "" for a nil error.
func errorString(err error) string {
	if err == nil {
		return ""
	}

	return err.Error()
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// errorJSON builds the {"error": msg} body shared by every JSON handler in
// this package that doesn't use configGetResponse's own Error field.
func errorJSON(msg string) map[string]string {
	return map[string]string{"error": msg}
}
