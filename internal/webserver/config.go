package webserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go-sync-objects/internal/config"
	"go-sync-objects/internal/configdb"
	"net/http"
	"net/url"
	"strings"

	"gopkg.in/yaml.v3"
)

// configGetResponse is what GET /api/config returns, and (with Applied/
// ApplyError additionally set) what POST /api/config returns too. Cfg is the
// loaded config (WebServer overlaid from the fixed bootstrap value for
// display) — always set on success, even when it's all zero values (e.g. a
// brand new install with nothing saved yet), so the structured editor always
// has something to render; Raw is the same config marshaled as YAML, offered
// as a raw-text editing mode. In both Cfg and Raw, API.Password and
// Database.DSN (which typically embeds the MariaDB credentials) are
// redacted (never sent back to the browser once saved — see redactSecrets)
// so stored secrets never round-trip into the config page's form fields.
// Error/no Cfg only happens on a genuine load failure (a database problem),
// not on an unconfigured-but-loadable state. Applied reports whether a
// POST's save was also successfully applied to the running process (see
// saveConfig); ApplyError carries why not, if the save itself succeeded but
// applying it live failed (e.g. an unreachable API or database) — the save
// is not rolled back in that case, only the live apply.
type configGetResponse struct {
	Cfg        *config.Config `json:"config,omitempty"`
	Raw        string         `json:"raw"`
	Error      string         `json:"error,omitempty"`
	Applied    bool           `json:"applied,omitempty"`
	ApplyError string         `json:"applyError,omitempty"`
}

// configSaveRequest is what POST /api/config accepts: either Raw (YAML
// text, unmarshaled) or Cfg (a structured config) — Raw takes priority if
// both are somehow set. Either way, the WebServer field of whatever's
// submitted is discarded server-side (see saveConfig): it's fixed by the
// bootstrap file and can't be changed through this API.
type configSaveRequest struct {
	Cfg *config.Config `json:"config,omitempty"`
	Raw *string        `json:"raw,omitempty"`
}

// configAPIHandler serves the JSON API the config page's script uses to load
// and save the SQLite-backed config in cfgDB. webServer is the fixed,
// bootstrap-sourced value overlaid onto every response and enforced (never
// read from the request) on every save. reload is called after every
// successful save so the change takes effect on the running process right
// away — see saveConfig.
func configAPIHandler(cfgDB *configdb.Store, webServer config.WebServer, reload func(context.Context) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			getConfig(r.Context(), w, cfgDB, webServer)
		case http.MethodPost:
			saveConfig(w, r, cfgDB, webServer, reload)
		default:
			w.Header().Set("Allow", http.MethodGet+", "+http.MethodPost)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	}
}

func getConfig(ctx context.Context, w http.ResponseWriter, cfgDB *configdb.Store, webServer config.WebServer) {
	cfg, err := cfgDB.Load(ctx)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, configGetResponse{Error: err.Error()})
		return
	}

	// Overlay for display/context only — never persisted (see saveConfig).
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

func saveConfig(
	w http.ResponseWriter, r *http.Request, cfgDB *configdb.Store, webServer config.WebServer,
	reload func(context.Context) error,
) {
	var req configSaveRequest

	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxConfigBodyBytes)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, configGetResponse{Error: fmt.Sprintf("decode request: %v", err)})
		return
	}

	cfg, err := configSaveConfig(req)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, configGetResponse{Error: err.Error()})
		return
	}

	// Discard whatever WebServer the client submitted before validating, so
	// a bad/irrelevant webServer value in the request can never even
	// surface as a validation error — it's fixed by the bootstrap file.
	cfg.WebServer = webServer

	// The config page never shows stored secrets back to the browser (see
	// getConfig/redactSecrets), so a blank password/DSN field means
	// "unchanged", not "clear it" — fill them back in from what's already
	// stored before validating/saving.
	if cfg.API.Password == "" || cfg.Database.DSN == "" {
		if err := fillStoredSecrets(r.Context(), cfgDB, cfg); err != nil {
			writeJSON(w, http.StatusInternalServerError, configGetResponse{Error: err.Error()})
			return
		}
	}

	if err := cfg.Validate(); err != nil {
		writeJSON(w, http.StatusBadRequest, configGetResponse{Error: err.Error()})
		return
	}

	if err := cfgDB.Save(r.Context(), cfg); err != nil {
		writeJSON(w, http.StatusInternalServerError, configGetResponse{Error: err.Error()})
		return
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

// configSaveConfig turns a configSaveRequest into a *config.Config: req.Raw
// (YAML-unmarshaled, unvalidated) if set, else req.Cfg directly. Validation
// happens once, uniformly, in saveConfig after the WebServer overlay.
func configSaveConfig(req configSaveRequest) (*config.Config, error) {
	if req.Raw != nil {
		var cfg config.Config
		if err := yaml.Unmarshal([]byte(*req.Raw), &cfg); err != nil {
			return nil, fmt.Errorf("parse raw YAML: %w", err)
		}

		return &cfg, nil
	}

	if req.Cfg == nil {
		return nil, errors.New("request has neither raw nor config")
	}

	return req.Cfg, nil
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
// leaving a field blank means "unchanged" (see saveConfig). A brand
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

// syncResponse is what POST /api/sync/{mapID} returns.
type syncResponse struct {
	OK     bool   `json:"ok"`
	Synced int    `json:"synced"`
	Error  string `json:"error,omitempty"`
}

// syncMapAPIHandler calls syncMap with the map ID taken from the URL path
// (the "/api/sync/" prefix registered in New) on each POST, running that one
// map's sync immediately (serialized against any concurrent scheduled sync —
// see runtime.runSyncMaps in main.go) instead of waiting for its next
// interval tick, and reports how many objects were synced or why it failed.
// It blocks for as long as the sync takes. This is what the status page's
// per-map "Sync" button calls.
func syncMapAPIHandler(syncMap func(context.Context, string) (int, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)

			return
		}

		id, err := url.PathUnescape(strings.TrimPrefix(r.URL.Path, "/api/sync/"))
		if err != nil || id == "" {
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
