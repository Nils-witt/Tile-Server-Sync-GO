package webserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go-sync-objects/internal/config"
	"go-sync-objects/internal/configdb"
	"net/http"

	"gopkg.in/yaml.v3"
)

// configGetResponse is what GET /api/config returns. Cfg is the loaded
// config (WebServer overlaid from the fixed bootstrap value for display) —
// always set on success, even when it's all zero values (e.g. a brand new
// install with nothing saved yet), so the structured editor always has
// something to render; Raw is the same config marshaled as YAML, offered as
// a raw-text editing mode. Error/no Cfg only happens on a genuine load
// failure (a database problem), not on an unconfigured-but-loadable state.
type configGetResponse struct {
	Cfg   *config.Config `json:"config,omitempty"`
	Raw   string         `json:"raw"`
	Error string         `json:"error,omitempty"`
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
// read from the request) on every save.
func configAPIHandler(cfgDB *configdb.Store, webServer config.WebServer) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			getConfig(r.Context(), w, cfgDB, webServer)
		case http.MethodPost:
			saveConfig(w, r, cfgDB, webServer)
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

func saveConfig(w http.ResponseWriter, r *http.Request, cfgDB *configdb.Store, webServer config.WebServer) {
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

	if err := cfg.Validate(); err != nil {
		writeJSON(w, http.StatusBadRequest, configGetResponse{Error: err.Error()})
		return
	}

	if err := cfgDB.Save(r.Context(), cfg); err != nil {
		writeJSON(w, http.StatusInternalServerError, configGetResponse{Error: err.Error()})
		return
	}

	raw, err := yaml.Marshal(cfg)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, configGetResponse{Error: err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, configGetResponse{Cfg: cfg, Raw: string(raw)})
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

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// reloadResponse is what POST /api/reload returns.
type reloadResponse struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

// reloadAPIHandler calls reload on each POST, reporting whether it
// succeeded. reload re-reads the config file and swaps it into the running
// process; see its doc comment (runtime.reload in main.go) for exactly what
// it does and doesn't cover.
func reloadAPIHandler(reload func(context.Context) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)

			return
		}

		if err := reload(r.Context()); err != nil {
			writeJSON(w, http.StatusBadRequest, reloadResponse{Error: err.Error()})
			return
		}

		writeJSON(w, http.StatusOK, reloadResponse{OK: true})
	}
}

// syncResponse is what POST /api/sync returns.
type syncResponse struct {
	OK     bool   `json:"ok"`
	Synced int    `json:"synced"`
	Error  string `json:"error,omitempty"`
}

// syncAPIHandler calls syncNow on each POST, running a sync immediately
// (serialized against any concurrent scheduled sync — see runtime.runSync in
// main.go) instead of waiting for the next interval tick, and reports how
// many objects were synced or why it failed. It blocks for as long as the
// sync takes.
func syncAPIHandler(syncNow func(context.Context) (int, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)

			return
		}

		synced, err := syncNow(r.Context())
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
