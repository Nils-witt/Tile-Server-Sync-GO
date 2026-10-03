package webserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/Nils-witt/Tile-Server-Sync-GO/internal/config"
	"github.com/Nils-witt/Tile-Server-Sync-GO/internal/configdb"
)

// configGetResponse is what each of the section save endpoints below
// returns. Cfg is the saved config — always set on success, even when
// it's all zero values (e.g. a brand new install with nothing saved yet), so
// the structured editor always has something to render. API.Password and
// Database.DSN (which typically embeds the MariaDB credentials) are
// redacted (never sent back to the browser once saved — see redactSecrets)
// so stored secrets never round-trip into the config page's form fields.
// Error/no Cfg only happens on a genuine load failure (a database problem),
// not on an unconfigured-but-loadable state. Applied reports whether a save
// was also successfully applied to the running process (see
// finishConfigSave); ApplyError carries why not, if the save itself
// succeeded but applying it live failed (e.g. an unreachable API or
// database) — the save is not rolled back in that case, only the live
// apply.
type configGetResponse struct {
	Cfg        *config.Config `json:"config,omitempty"`
	Error      string         `json:"error,omitempty"`
	Applied    bool           `json:"applied,omitempty"`
	ApplyError string         `json:"applyError,omitempty"`
}

const maxConfigBodyBytes = 1 << 20 // 1 MiB; config is never remotely this large

// apiSectionRequest/databaseSectionRequest are the request/response bodies
// for the API/Database section endpoints — each submits or returns only its
// own tab's fields. GET /api/config/{api,database} return the same shape their PUT counterpart
// expects (see getAPISectionHandler/getDatabaseSectionHandler below).
type apiSectionRequest struct {
	API config.API `json:"api"`
}

type databaseSectionRequest struct {
	Database config.Database `json:"database"`
}

// getSectionHandler builds the handler behind GET /api/config/api and GET
// /api/config/database: load the stored config, redact its secrets the same
// way the bundle GET does, and return just what extract picks out of it.
func getSectionHandler(cfgDB *configdb.Store, extract func(cfg *config.Config) any) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cfg, err := cfgDB.Load(r.Context())
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, errorJSON(err.Error()))
			return
		}

		redactSecrets(cfg)
		writeJSON(w, http.StatusOK, extract(cfg))
	}
}

// getAPISectionHandler serves GET /api/config/api. Requires view_config.
func getAPISectionHandler(cfgDB *configdb.Store) http.HandlerFunc {
	return getSectionHandler(cfgDB, func(cfg *config.Config) any { return apiSectionRequest{API: cfg.API} })
}

// getDatabaseSectionHandler serves GET /api/config/database. Requires
// view_config.
func getDatabaseSectionHandler(cfgDB *configdb.Store) http.HandlerFunc {
	return getSectionHandler(cfgDB,
		func(cfg *config.Config) any { return databaseSectionRequest{Database: cfg.Database} })
}

// sectionSaveHandler is the shared shape behind saveAPISectionHandler/
// saveDatabaseSectionHandler: decode the request body via decode, then merge
// just that section into the stored config via saveConfigSection. The
// method itself is already guaranteed by the "PUT /api/config/{section}" mux
// pattern registered in webserver.go, so there's no method check here.
// section is a short label ("api", "database") recorded in the security log
// by finishConfigSave. merge also returns a human-readable description of
// what it changed (see the diff* helpers in audit_diff.go), also recorded
// there.
func sectionSaveHandler(
	cfgDB *configdb.Store, reload func(context.Context) error, section string,
	decode func(w http.ResponseWriter, r *http.Request) (merge func(cfg *config.Config) []string, err error),
) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		merge, err := decode(w, r)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, configGetResponse{Error: err.Error()})
			return
		}

		saveConfigSection(w, r, cfgDB, reload, section, merge)
	}
}

// decodeSectionHandler builds a sectionSaveHandler that decodes its request
// body as a T and hands it to merge, factoring out the otherwise-identical
// decode-then-merge shape shared by every section save endpoint below. merge
// returns the section's before value's diff against what it just wrote into
// cfg.
func decodeSectionHandler[T any](
	cfgDB *configdb.Store, reload func(context.Context) error, section string,
	merge func(cfg *config.Config, req T) []string,
) http.HandlerFunc {
	return sectionSaveHandler(cfgDB, reload, section, func(w http.ResponseWriter, r *http.Request) (func(*config.Config) []string, error) {
		var req T
		if err := decodeConfigBody(w, r, &req); err != nil {
			return nil, err
		}

		return func(cfg *config.Config) []string { return merge(cfg, req) }, nil
	})
}

// saveAPISectionHandler serves POST /api/config/api: loads the currently
// stored config, replaces just its API section with the request body, and
// saves it. Requires edit_config_api (enforced at the route level).
func saveAPISectionHandler(
	cfgDB *configdb.Store, reload func(context.Context) error,
) http.HandlerFunc {
	return decodeSectionHandler(cfgDB, reload, "api",
		func(cfg *config.Config, req apiSectionRequest) []string {
			changes := diffAPI(cfg.API, req.API)
			cfg.API = req.API

			return changes
		})
}

// saveDatabaseSectionHandler serves POST /api/config/database, the Database
// analogue of saveAPISectionHandler. Requires edit_config_database.
func saveDatabaseSectionHandler(
	cfgDB *configdb.Store, reload func(context.Context) error,
) http.HandlerFunc {
	return decodeSectionHandler(cfgDB, reload, "database",
		func(cfg *config.Config, req databaseSectionRequest) []string {
			changes := diffDatabase(cfg.Database, req.Database)
			cfg.Database = req.Database

			return changes
		})
}

func decodeConfigBody(w http.ResponseWriter, r *http.Request, v any) error {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxConfigBodyBytes)).Decode(v); err != nil {
		return fmt.Errorf("decode request: %w", err)
	}

	return nil
}

// saveConfigSection loads the currently stored config, applies merge (which
// overwrites just one section and reports what it changed), and hands off to
// finishConfigSave. Loading first means every section untouched by merge
// keeps its existing stored value, matching each tab's "save just this tab"
// semantics. The stored secrets are captured from that same load before
// merge overwrites them, so finishConfigSave can restore a blank one without
// loading the config a second time.
func saveConfigSection(
	w http.ResponseWriter, r *http.Request, cfgDB *configdb.Store,
	reload func(context.Context) error, section string, merge func(cfg *config.Config) []string,
) {
	cfg, err := cfgDB.Load(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, configGetResponse{Error: err.Error()})
		return
	}

	stored := storedSecrets{apiPassword: cfg.API.Password, databaseDSN: cfg.Database.DSN}
	changes := merge(cfg)
	finishConfigSave(w, r, cfgDB, reload, section, changes, cfg, stored)
}

// finishConfigSave is the common tail shared by every section save: fill
// back in any secret left blank (meaning "unchanged" — see storedSecrets),
// save, and apply the change live via reload.
//
// Deliberately not gated on cfg.Validate(): Config.Validate requires the
// *whole* config to be complete (api.baseUrl, api credentials, database.dsn —
// see internal/config's Validate; maps may be empty), which a single section
// save can never satisfy on its own during initial setup — saving just the
// API tab would always fail because Database isn't filled in yet, and vice
// versa, so nothing could ever be saved for the first time.
// Instead, an incomplete-but-persisted config is simply not applied live:
// reload() below runs Validate() itself and reports why via ApplyError
// (the same "saved, but not yet applied" outcome already used for a
// valid-but-unreachable API/database), so each tab's edit is never lost
// while the other tabs are still being filled in.
func finishConfigSave(
	w http.ResponseWriter, r *http.Request, cfgDB *configdb.Store,
	reload func(context.Context) error, section string, changes []string, cfg *config.Config, stored storedSecrets,
) {
	stored.fill(cfg)

	if err := cfgDB.Save(r.Context(), cfg); err != nil {
		writeJSON(w, http.StatusInternalServerError, configGetResponse{Error: err.Error()})
		return
	}

	if actor, ok := currentUser(r.Context()); ok {
		logSecurityEvent(r, cfgDB, "config_saved", actor.Username, "section="+section+"; "+changesDetail(changes))
	}

	redactSecrets(cfg)

	resp := configGetResponse{Cfg: cfg}

	if applyErr := reload(r.Context()); applyErr != nil {
		resp.ApplyError = applyErr.Error()
	} else {
		resp.Applied = true
	}

	writeJSON(w, http.StatusOK, resp)
}

// redactSecrets clears cfg.API.Password and cfg.Database.DSN in place before
// a *config.Config is sent to the browser, so stored secrets are never
// echoed back into the config page — see configGetResponse and
// storedSecrets. DSN is included
// because it typically embeds the MariaDB username/password (e.g.
// "user:pass@tcp(...)"), not just a host/database name.
func redactSecrets(cfg *config.Config) {
	cfg.API.Password = ""
	cfg.Database.DSN = ""
}

// storedSecrets holds the API password and database DSN as stored before a
// section save. The config page never shows the real values back to the
// browser (see redactSecrets), so a blank field in a save request means
// "unchanged", not "clear it". A brand new/unconfigured install has no stored
// values to fall back to, which is fine: the field just stays empty, exactly
// as if the user had typed nothing.
type storedSecrets struct {
	apiPassword string
	databaseDSN string
}

// fill restores each secret cfg left blank from s.
func (s storedSecrets) fill(cfg *config.Config) {
	if cfg.API.Password == "" {
		cfg.API.Password = s.apiPassword
	}

	if cfg.Database.DSN == "" {
		cfg.Database.DSN = s.databaseDSN
	}
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
