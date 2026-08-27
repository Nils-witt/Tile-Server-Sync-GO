package webserver

import (
	"go-sync-objects/internal/configdb"
	"net/http"
)

// ssoConfigDTO is configdb.SSOConfig as sent to/from the JSON API. ClientID
// is not treated as a secret (it's visible in every browser redirect to the
// provider anyway), but ClientSecret is redacted the same way
// config.go redacts API.Password/Database.DSN: never echoed back once
// saved, and a blank value on save means "leave the stored secret
// unchanged".
type ssoConfigDTO struct {
	Enabled            bool                 `json:"enabled"`
	IssuerURL          string               `json:"issuerUrl"`
	ClientID           string               `json:"clientId"`
	ClientSecret       string               `json:"clientSecret"`
	Scopes             string               `json:"scopes"`
	ButtonLabel        string               `json:"buttonLabel"`
	RedirectBaseURL    string               `json:"redirectBaseUrl"`
	DefaultPermissions configdb.Permissions `json:"defaultPermissions"`
}

func toSSOConfigDTO(cfg *configdb.SSOConfig) ssoConfigDTO {
	return ssoConfigDTO{
		Enabled: cfg.Enabled, IssuerURL: cfg.IssuerURL, ClientID: cfg.ClientID,
		Scopes: cfg.Scopes, ButtonLabel: cfg.ButtonLabel, RedirectBaseURL: cfg.RedirectBaseURL,
		DefaultPermissions: cfg.DefaultPermissions,
	}
}

const defaultSSOScopes = "openid profile email"

const defaultSSOButtonLabel = "Sign in with SSO"

// getSSOConfigHandler serves GET /api/config/sso: the SSO tab's data, with
// ClientSecret always blank (see ssoConfigDTO). Gated by permViewConfig,
// same as the rest of /config's data.
func getSSOConfigHandler(cfgDB *configdb.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cfg, err := cfgDB.LoadSSOConfig(r.Context())
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, errorJSON(err.Error()))
			return
		}

		writeJSON(w, http.StatusOK, toSSOConfigDTO(cfg))
	}
}

// saveSSOConfigHandler serves POST /api/config/sso: decodes the tab's fields,
// fills a blank ClientSecret back in from the stored value (see
// ssoConfigDTO), applies defaults for a blank Scopes/ButtonLabel, and saves.
// Gated by permEditConfigSSO. There's no live "reload"/"applied" concept
// here (unlike finishConfigSave in config.go): nothing about SSO is cached
// in the running process, so the very next /login/sso simply reads the new
// row.
func saveSSOConfigHandler(cfgDB *configdb.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req ssoConfigDTO
		if err := decodeConfigBody(w, r, &req); err != nil {
			writeJSON(w, http.StatusBadRequest, errorJSON(err.Error()))
			return
		}

		cfg := configdb.SSOConfig{
			Enabled: req.Enabled, IssuerURL: req.IssuerURL, ClientID: req.ClientID, ClientSecret: req.ClientSecret,
			Scopes: req.Scopes, ButtonLabel: req.ButtonLabel, RedirectBaseURL: req.RedirectBaseURL,
			DefaultPermissions: req.DefaultPermissions,
		}

		// Loaded unconditionally (not just when ClientSecret is blank) so it
		// also serves as the "before" side of the diff recorded in the
		// security log below.
		stored, err := cfgDB.LoadSSOConfig(r.Context())
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, errorJSON(err.Error()))
			return
		}

		if cfg.ClientSecret == "" {
			cfg.ClientSecret = stored.ClientSecret
		}

		if cfg.Scopes == "" {
			cfg.Scopes = defaultSSOScopes
		}

		if cfg.ButtonLabel == "" {
			cfg.ButtonLabel = defaultSSOButtonLabel
		}

		if err := cfgDB.SaveSSOConfig(r.Context(), &cfg); err != nil {
			writeJSON(w, http.StatusInternalServerError, errorJSON(err.Error()))
			return
		}

		if actor, ok := currentUser(r.Context()); ok {
			changes := diffSSO(stored, &cfg)
			logSecurityEvent(r, cfgDB, "config_saved", actor.Username, "section=sso; "+changesDetail(changes))
		}

		writeJSON(w, http.StatusOK, toSSOConfigDTO(&cfg))
	}
}

// ssoStatusResponse is what the public GET /api/sso/status returns: just
// enough for the unauthenticated login page to decide whether to show an
// SSO button and with what label. Never includes provider details.
type ssoStatusResponse struct {
	Enabled     bool   `json:"enabled"`
	ButtonLabel string `json:"buttonLabel"`
}

// ssoStatusAPIHandler serves GET /api/sso/status. Deliberately not gated
// behind requireUser/requirePermission: the login page needs this before a
// session exists, same as /login itself.
func ssoStatusAPIHandler(cfgDB *configdb.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)

			return
		}

		cfg, err := cfgDB.LoadSSOConfig(r.Context())
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, errorJSON(err.Error()))
			return
		}

		label := cfg.ButtonLabel
		if label == "" {
			label = defaultSSOButtonLabel
		}

		writeJSON(w, http.StatusOK, ssoStatusResponse{Enabled: cfg.Enabled, ButtonLabel: label})
	}
}

// ssoConfigAPIHandler serves both GET and POST /api/config/sso, applying a
// different required permission per method (view vs edit) rather than
// composing two requirePermission-wrapped handlers on the same path, since
// net/http's ServeMux keys a plain pattern on path alone.
func ssoConfigAPIHandler(cfgDB *configdb.Store) http.HandlerFunc {
	get := requirePermission(cfgDB, false, permViewConfig)(getSSOConfigHandler(cfgDB))
	post := requirePermission(cfgDB, false, permEditConfigSSO)(saveSSOConfigHandler(cfgDB))

	return func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			get(w, r)
		case http.MethodPost:
			post(w, r)
		default:
			w.Header().Set("Allow", http.MethodGet+", "+http.MethodPost)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	}
}
