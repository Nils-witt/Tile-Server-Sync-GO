package webserver

import (
	"context"
	"encoding/json"
	"fmt"
	"go-sync-objects/internal/config"
	"go-sync-objects/internal/configdb"
	"net/http"

	"gopkg.in/yaml.v3"
)

// configGetResponse is what GET /api/config returns, and (with Applied/
// ApplyError additionally set) what each of the section save endpoints
// below returns too. Cfg is the loaded config (WebServer overlaid from the
// fixed bootstrap value for display) — always set on success, even when
// it's all zero values (e.g. a brand new install with nothing saved yet), so
// the structured editor always has something to render; Raw is the same
// config marshaled as YAML, offered as a raw-text editing mode. In both Cfg
// and Raw, API.Password and Database.DSN (which typically embeds the
// MariaDB credentials) are redacted (never sent back to the browser once
// saved — see redactSecrets) so stored secrets never round-trip into the
// config page's form fields. Error/no Cfg only happens on a genuine load
// failure (a database problem), not on an unconfigured-but-loadable state.
// Applied reports whether a save was also successfully applied to the
// running process (see finishConfigSave); ApplyError carries why not, if
// the save itself succeeded but applying it live failed (e.g. an
// unreachable API or database) — the save is not rolled back in that case,
// only the live apply.
type configGetResponse struct {
	Cfg        *config.Config `json:"config,omitempty"`
	Raw        string         `json:"raw"`
	Error      string         `json:"error,omitempty"`
	Applied    bool           `json:"applied,omitempty"`
	ApplyError string         `json:"applyError,omitempty"`
}

// configAPIHandler serves GET /api/config, the JSON the config page's script
// uses to populate the API/Database tabs (the Maps tab now sources its data
// from GET /api/maps instead — see maps.go) plus the raw-YAML bundle. Saving
// is done per-section instead — see saveAPISectionHandler/
// saveDatabaseSectionHandler — so each tab's edit permission is enforced
// independently at the route level (see webserver.go).
func configAPIHandler(cfgDB *configdb.Store, webServer config.WebServer) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		getConfig(r.Context(), w, cfgDB, webServer)
	}
}

func getConfig(ctx context.Context, w http.ResponseWriter, cfgDB *configdb.Store, webServer config.WebServer) {
	cfg, err := cfgDB.Load(ctx)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, configGetResponse{Error: err.Error()})
		return
	}

	// Overlay for display/context only — never persisted (see
	// finishConfigSave).
	cfg.WebServer = webServer
	redactSecrets(cfg)

	resp := configGetResponse{Cfg: cfg}

	raw, err := yaml.Marshal(cfg)
	if err != nil {
		resp.Error = err.Error()
	} else {
		resp.Raw = string(raw)
	}

	writeJSON(w, http.StatusOK, resp)
}

const maxConfigBodyBytes = 1 << 20 // 1 MiB; config is never remotely this large

// apiSectionRequest/databaseSectionRequest are the request/response bodies
// for the API/Database section endpoints — each submits or returns only its
// own tab's fields, unlike the whole-config GET /api/config bundle. GET
// /api/config/{api,database} return the same shape their PUT counterpart
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
	cfgDB *configdb.Store, webServer config.WebServer, reload func(context.Context) error, section string,
	decode func(w http.ResponseWriter, r *http.Request) (merge func(cfg *config.Config) []string, err error),
) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		merge, err := decode(w, r)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, configGetResponse{Error: err.Error()})
			return
		}

		saveConfigSection(w, r, cfgDB, webServer, reload, section, merge)
	}
}

// decodeSectionHandler builds a sectionSaveHandler that decodes its request
// body as a T and hands it to merge, factoring out the otherwise-identical
// decode-then-merge shape shared by every section save endpoint below. merge
// returns the section's before value's diff against what it just wrote into
// cfg.
func decodeSectionHandler[T any](
	cfgDB *configdb.Store, webServer config.WebServer, reload func(context.Context) error, section string,
	merge func(cfg *config.Config, req T) []string,
) http.HandlerFunc {
	return sectionSaveHandler(cfgDB, webServer, reload, section, func(w http.ResponseWriter, r *http.Request) (func(*config.Config) []string, error) {
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
	cfgDB *configdb.Store, webServer config.WebServer, reload func(context.Context) error,
) http.HandlerFunc {
	return decodeSectionHandler(cfgDB, webServer, reload, "api",
		func(cfg *config.Config, req apiSectionRequest) []string {
			changes := diffAPI(cfg.API, req.API)
			cfg.API = req.API

			return changes
		})
}

// saveDatabaseSectionHandler serves POST /api/config/database, the Database
// analogue of saveAPISectionHandler. Requires edit_config_database.
func saveDatabaseSectionHandler(
	cfgDB *configdb.Store, webServer config.WebServer, reload func(context.Context) error,
) http.HandlerFunc {
	return decodeSectionHandler(cfgDB, webServer, reload, "database",
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
// semantics.
func saveConfigSection(
	w http.ResponseWriter, r *http.Request, cfgDB *configdb.Store, webServer config.WebServer,
	reload func(context.Context) error, section string, merge func(cfg *config.Config) []string,
) {
	cfg, err := cfgDB.Load(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, configGetResponse{Error: err.Error()})
		return
	}

	changes := merge(cfg)
	finishConfigSave(w, r, cfgDB, webServer, reload, section, changes, cfg)
}

// finishConfigSave is the common tail shared by every save path (per-section
// and raw): discard/overwrite WebServer with the fixed bootstrap value, fill
// back in any secret left blank (meaning "unchanged" — see
// fillStoredSecrets), save, and apply the change live via reload.
//
// Deliberately not gated on cfg.Validate(): Config.Validate requires the
// *whole* config to be complete (api.baseUrl, api credentials, database.dsn,
// at least one map — see internal/config's Validate), which a single
// section save can never satisfy on its own during initial setup — saving
// just the API tab would always fail because Database/Maps aren't filled in
// yet, and vice versa, so nothing could ever be saved for the first time.
// Instead, an incomplete-but-persisted config is simply not applied live:
// reload() below runs Validate() itself and reports why via ApplyError
// (the same "saved, but not yet applied" outcome already used for a
// valid-but-unreachable API/database), so each tab's edit is never lost
// while the other tabs are still being filled in.
func finishConfigSave(
	w http.ResponseWriter, r *http.Request, cfgDB *configdb.Store, webServer config.WebServer,
	reload func(context.Context) error, section string, changes []string, cfg *config.Config,
) {
	// Discard whatever WebServer was submitted (or, for a raw save,
	// unmarshaled) before saving, so a bad/irrelevant webServer value can
	// never take effect — it's fixed by the bootstrap file.
	cfg.WebServer = webServer

	// The config page never shows stored secrets back to the browser (see
	// getConfig/redactSecrets), so a blank password/DSN means "unchanged",
	// not "clear it" — fill them back in from what's already stored before
	// saving.
	if cfg.API.Password == "" || cfg.Database.DSN == "" {
		if err := fillStoredSecrets(r.Context(), cfgDB, cfg); err != nil {
			writeJSON(w, http.StatusInternalServerError, configGetResponse{Error: err.Error()})
			return
		}
	}

	if err := cfgDB.Save(r.Context(), cfg); err != nil {
		writeJSON(w, http.StatusInternalServerError, configGetResponse{Error: err.Error()})
		return
	}

	if actor, ok := currentUser(r.Context()); ok {
		logSecurityEvent(r, cfgDB, "config_saved", actor.Username, "section="+section+"; "+changesDetail(changes))
	}

	redactSecrets(cfg)

	raw, err := yaml.Marshal(cfg)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, configGetResponse{Error: err.Error()})
		return
	}

	resp := configGetResponse{Cfg: cfg, Raw: string(raw)}

	if applyErr := reload(r.Context()); applyErr != nil {
		resp.ApplyError = applyErr.Error()
	} else {
		resp.Applied = true
	}

	writeJSON(w, http.StatusOK, resp)
}

// redactSecrets clears cfg.API.Password and cfg.Database.DSN in place before
// a *config.Config is sent to the browser (in either JSON or the Raw YAML
// alongside it), so stored secrets are never echoed back into the config
// page — see configGetResponse and fillStoredSecrets. DSN is included
// because it typically embeds the MariaDB username/password (e.g.
// "user:pass@tcp(...)"), not just a host/database name.
func redactSecrets(cfg *config.Config) {
	cfg.API.Password = ""
	cfg.Database.DSN = ""
}

// fillStoredSecrets fills cfg.API.Password and/or cfg.Database.DSN in from
// the currently stored config when a save request submits either blank,
// since the config page never shows the real values back to the browser, so
// leaving a field blank means "unchanged" (see finishConfigSave). A brand
// new/unconfigured install has no stored values to fall back to, which is
// fine: the field just stays empty, exactly as if the user had typed
// nothing.
func fillStoredSecrets(ctx context.Context, cfgDB *configdb.Store, cfg *config.Config) error {
	stored, err := cfgDB.Load(ctx)
	if err != nil {
		return fmt.Errorf("load stored config: %w", err)
	}

	if cfg.API.Password == "" {
		cfg.API.Password = stored.API.Password
	}

	if cfg.Database.DSN == "" {
		cfg.Database.DSN = stored.Database.DSN
	}

	return nil
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

func configPageHandler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/config" {
		http.NotFound(w, r)
		return
	}

	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)

		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(configPageHTML))
}
