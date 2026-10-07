package config

import "errors"

const (
	// DefaultSSOScopes is used when oidc.scopes is left empty.
	DefaultSSOScopes = "openid profile email"
	// DefaultSSOButtonLabel is used when oidc.buttonLabel is left empty.
	DefaultSSOButtonLabel = "Sign in with SSO"
	// DefaultSSOGroupsClaim is used when oidc.groupsClaim is left empty.
	DefaultSSOGroupsClaim = "groups"
)

// SSO is the OpenID Connect single sign-on configuration: the web server's
// only login method, so it's always on and IssuerURL/ClientID are required.
// It's read from the bootstrap file (see Bootstrap), not from the SQLite
// config database, and not editable through the web UI, so changing it needs a restart. The
// SPA runs the login itself as a public client (authorization code + PKCE,
// no client secret) and then authenticates every API call with the
// provider's access token; see internal/webserver/sso_bearer.go.
type SSO struct {
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
	// DefaultPermissions is granted to every signed-in SSO user, on top of
	// whatever their groups grant (see GroupPermissions). It has no
	// superuser field, so superuser can only ever come from a group.
	DefaultPermissions SSOPermissions `yaml:"defaultPermissions" json:"defaultPermissions"`
	// GroupsClaim names the access-token claim listing the user's groups
	// (a string array, or a single string). Defaults to DefaultSSOGroupsClaim.
	GroupsClaim string `yaml:"groupsClaim" json:"groupsClaim"`
	// GroupPermissions maps a group name (as it appears in GroupsClaim) to
	// the permissions (and optionally superuser) its members get, on top of
	// DefaultPermissions. Nothing about users is stored locally, so these two
	// are the only source of permissions there is. They are
	// worked out from the token on every request, so a group change at the
	// provider applies on the next request and removing someone from a group
	// revokes what it granted.
	GroupPermissions map[string]SSOGroupGrant `yaml:"groupPermissions" json:"groupPermissions"`
}

// SSOPermissions is the set of independently grantable feature permissions
// defaultPermissions and each groupPermissions entry hand out, and what internal/webserver checks each
// route against. It deliberately has no umbrella "edit config" flag: editing
// is only ever granted per-section (API/Database/Maps), matching the
// section-specific save endpoints.
type SSOPermissions struct {
	ViewStatus         bool `yaml:"viewStatus" json:"viewStatus"`
	TriggerSync        bool `yaml:"triggerSync" json:"triggerSync"`
	ViewConfig         bool `yaml:"viewConfig" json:"viewConfig"`
	EditConfigAPI      bool `yaml:"editConfigApi" json:"editConfigApi"`
	EditConfigDatabase bool `yaml:"editConfigDatabase" json:"editConfigDatabase"`
	EditConfigMaps     bool `yaml:"editConfigMaps" json:"editConfigMaps"`
}

// SSOGroupGrant is one oidc.groupPermissions entry: the six permissions,
// written inline next to an optional superuser flag (which gates the
// security log only).
type SSOGroupGrant struct {
	SSOPermissions `yaml:",inline"`
	Superuser      bool `yaml:"superuser" json:"superuser"`
}

// applyDefaults fills in a blank Scopes/ButtonLabel/GroupsClaim.
func (s *SSO) applyDefaults() {
	if s.GroupsClaim == "" {
		s.GroupsClaim = DefaultSSOGroupsClaim
	}

	if s.Scopes == "" {
		s.Scopes = DefaultSSOScopes
	}

	if s.ButtonLabel == "" {
		s.ButtonLabel = DefaultSSOButtonLabel
	}
}

// validate requires the provider settings SSO can't work without.
func (s *SSO) validate() error {
	var errs []error
	if s.IssuerURL == "" {
		errs = append(errs, errors.New("oidc.issuerUrl is required"))
	}

	if s.ClientID == "" {
		errs = append(errs, errors.New("oidc.clientId is required"))
	}

	for group := range s.GroupPermissions {
		if group == "" {
			errs = append(errs, errors.New("oidc.groupPermissions: group name must not be empty"))
		}
	}

	return errors.Join(errs...)
}
