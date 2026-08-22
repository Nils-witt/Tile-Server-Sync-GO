package webserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go-sync-objects/internal/config"
	"net/http"
	"os"

	"gopkg.in/yaml.v3"
)

// configGetResponse is what GET /api/config returns. Cfg is the parsed,
// fully-defaulted config for the structured editor; Raw is always the exact
// file content, used as a fallback the config page falls back to editing
// directly (as YAML text) when the file doesn't currently parse (e.g. it was
// hand-edited into an invalid state since the process started).
type configGetResponse struct {
	Cfg   *config.Config `json:"config,omitempty"`
	Raw   string         `json:"raw"`
	Error string         `json:"error,omitempty"`
}

// configSaveRequest is what POST /api/config accepts: either Raw (YAML text,
// written to the file verbatim after validating it parses) or Cfg (a
// structured config, marshaled to YAML before writing). Raw takes priority
// if both are somehow set.
type configSaveRequest struct {
	Cfg *config.Config `json:"config,omitempty"`
	Raw *string        `json:"raw,omitempty"`
}

// configAPIHandler serves the JSON API the config page's script uses to load
// and save configPath.
func configAPIHandler(configPath string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			getConfig(w, configPath)
		case http.MethodPost:
			saveConfig(w, r, configPath)
		default:
			w.Header().Set("Allow", http.MethodGet+", "+http.MethodPost)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	}
}

func getConfig(w http.ResponseWriter, configPath string) {
	data, err := os.ReadFile(configPath) //nolint:gosec // configPath is a trusted, user-supplied CLI flag
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, configGetResponse{Error: err.Error()})
		return
	}

	resp := configGetResponse{Raw: string(data)}

	cfg, err := config.Parse(data)
	if err != nil {
		resp.Error = err.Error()
	} else {
		resp.Cfg = cfg
	}

	writeJSON(w, http.StatusOK, resp)
}

const maxConfigBodyBytes = 1 << 20 // 1 MiB; config.yaml is never remotely this large

func saveConfig(w http.ResponseWriter, r *http.Request, configPath string) {
	var req configSaveRequest

	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxConfigBodyBytes)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, configGetResponse{Error: fmt.Sprintf("decode request: %v", err)})
		return
	}

	data, err := configSaveBytes(req)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, configGetResponse{Error: err.Error()})
		return
	}

	if _, err := config.Parse(data); err != nil {
		writeJSON(w, http.StatusBadRequest, configGetResponse{Error: err.Error(), Raw: string(data)})
		return
	}

	if err := os.WriteFile(configPath, data, 0o600); err != nil {
		writeJSON(w, http.StatusInternalServerError, configGetResponse{Error: err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, configGetResponse{Raw: string(data)})
}

// configSaveBytes turns a configSaveRequest into the YAML bytes to validate
// and write: req.Raw verbatim if set, otherwise req.Cfg marshaled to YAML.
func configSaveBytes(req configSaveRequest) ([]byte, error) {
	if req.Raw != nil {
		return []byte(*req.Raw), nil
	}

	if req.Cfg == nil {
		return nil, errors.New("request has neither raw nor config")
	}

	data, err := yaml.Marshal(req.Cfg)
	if err != nil {
		return nil, fmt.Errorf("encode config as YAML: %w", err)
	}

	return data, nil
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
