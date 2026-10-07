package webserver

import (
	"net/http"

	"github.com/Nils-witt/Tile-Server-Sync-GO/internal/config"
)

// ssoStatusResponse is what the public GET /api/sso/status returns: what the
// unauthenticated login page needs to show an SSO button and run the OIDC
// flow itself as a public client (see sso_bearer.go). None of the fields are
// secret — they
// appear in every authorization redirect to the provider anyway.
type ssoStatusResponse struct {
	ButtonLabel string `json:"buttonLabel"`
	IssuerURL   string `json:"issuerUrl"`
	ClientID    string `json:"clientId"`
	Scopes      string `json:"scopes"`
}

// ssoStatusAPIHandler serves GET /api/sso/status from the bootstrap file's
// SSO section (config.SSO — already defaulted and validated by
// config.LoadBootstrap). Deliberately not gated behind requireUser/
// requirePermission: the login page needs this before any credential
// exists.
func ssoStatusAPIHandler(sso config.SSO) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, ssoStatusResponse{
			ButtonLabel: sso.ButtonLabel, IssuerURL: sso.IssuerURL, ClientID: sso.ClientID,
			Scopes: sso.Scopes,
		})
	}
}
