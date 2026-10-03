package config

import "errors"

const (
	// DefaultSSOScopes is used when oidc.scopes is left empty.
	DefaultSSOScopes = "openid profile email"
	// DefaultSSOButtonLabel is used when oidc.buttonLabel is left empty.
	DefaultSSOButtonLabel = "Sign in with SSO"
)

// SSO is the optional OpenID Connect single sign-on configuration, read from
// the bootstrap file (see Bootstrap) — not from the SQLite config database,
// and not editable through the web UI, so changing it needs a restart. The
// SPA runs the login itself as a public client (authorization code + PKCE,
// no client secret) and then authenticates every API call with the
// provider's access token; see internal/webserver/sso_bearer.go.
type SSO struct {
	Enabled bool `yaml:"enabled" json:"enabled"`
	// IssuerURL is the provider's issuer, e.g. "https://accounts.example.com"
	// — the base for OIDC discovery, and what every token's "iss" must match.
	IssuerURL string `yaml:"issuerUrl" json:"issuerUrl"`
	// ClientID is the public client the SPA logs in as; a token must name
	// it as its "azp" or in its "aud".
	ClientID string `yaml:"clientId" json:"clientId"`
	// Scopes is a space-separated OAuth2 scope list. Defaults to
	// DefaultSSOScopes.
	Scopes string `yaml:"scopes" json:"scopes"`
	// ButtonLabel is the login page's SSO button text. Defaults to
	// DefaultSSOButtonLabel.
	ButtonLabel string `yaml:"buttonLabel" json:"buttonLabel"`
	// DefaultPermissions is the permission set a first-time SSO login's
	// auto-provisioned account gets. An SSO account is never made a superuser
	// automatically.
	DefaultPermissions SSOPermissions `yaml:"defaultPermissions" json:"defaultPermissions"`
}

// SSOPermissions mirrors configdb.Permissions field for field (this package
// can't import configdb, which already imports it); internal/webserver
// converts between the two.
type SSOPermissions struct {
	ViewStatus         bool `yaml:"viewStatus" json:"viewStatus"`
	TriggerSync        bool `yaml:"triggerSync" json:"triggerSync"`
	ViewConfig         bool `yaml:"viewConfig" json:"viewConfig"`
	EditConfigAPI      bool `yaml:"editConfigApi" json:"editConfigApi"`
	EditConfigDatabase bool `yaml:"editConfigDatabase" json:"editConfigDatabase"`
	EditConfigMaps     bool `yaml:"editConfigMaps" json:"editConfigMaps"`
}

// applyDefaults fills in a blank Scopes/ButtonLabel.
func (s *SSO) applyDefaults() {
	if s.Scopes == "" {
		s.Scopes = DefaultSSOScopes
	}

	if s.ButtonLabel == "" {
		s.ButtonLabel = DefaultSSOButtonLabel
	}
}

// validate requires the provider settings an enabled SSO can't work without.
// A disabled section isn't checked at all.
func (s *SSO) validate() error {
	if !s.Enabled {
		return nil
	}

	var errs []error
	if s.IssuerURL == "" {
		errs = append(errs, errors.New("oidc.issuerUrl is required when oidc.enabled is true"))
	}

	if s.ClientID == "" {
		errs = append(errs, errors.New("oidc.clientId is required when oidc.enabled is true"))
	}

	return errors.Join(errs...)
}
