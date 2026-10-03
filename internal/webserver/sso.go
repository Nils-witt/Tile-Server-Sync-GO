package webserver

import (
	"Tile-Server-Sync-GO/internal/config"
	"Tile-Server-Sync-GO/internal/configdb"
	"net/http"
)

// ssoStatusResponse is what the public GET /api/sso/status returns: what the
// unauthenticated login page needs to show an SSO button and run the OIDC
// flow itself as a public client (see sso_bearer.go). The provider fields
// are only filled in while SSO is enabled; none of them are secret — they
// appear in every authorization redirect to the provider anyway.
type ssoStatusResponse struct {
	Enabled     bool   `json:"enabled"`
	ButtonLabel string `json:"buttonLabel"`
	IssuerURL   string `json:"issuerUrl,omitempty"`
	ClientID    string `json:"clientId,omitempty"`
	Scopes      string `json:"scopes,omitempty"`
}

// ssoStatusAPIHandler serves GET /api/sso/status from the bootstrap file's
// SSO section (config.SSO — already defaulted and validated by
// config.LoadBootstrap). Deliberately not gated behind requireUser/
// requirePermission: the login page needs this before any credential
// exists, same as /api/login itself.
func ssoStatusAPIHandler(sso config.SSO) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		if !sso.Enabled {
			writeJSON(w, http.StatusOK, ssoStatusResponse{Enabled: false, ButtonLabel: sso.ButtonLabel})
			return
		}

		writeJSON(w, http.StatusOK, ssoStatusResponse{
			Enabled: true, ButtonLabel: sso.ButtonLabel, IssuerURL: sso.IssuerURL, ClientID: sso.ClientID,
			Scopes: sso.Scopes,
		})
	}
}

// ssoDefaultPermissions converts one of the bootstrap file's SSO permission
// sets (defaultPermissions, or a groupPermissions entry) into configdb's own
// type.
func ssoDefaultPermissions(p config.SSOPermissions) configdb.Permissions {
	return configdb.Permissions{
		ViewStatus: p.ViewStatus, TriggerSync: p.TriggerSync, ViewConfig: p.ViewConfig,
		EditConfigAPI: p.EditConfigAPI, EditConfigDatabase: p.EditConfigDatabase, EditConfigMaps: p.EditConfigMaps,
	}
}
