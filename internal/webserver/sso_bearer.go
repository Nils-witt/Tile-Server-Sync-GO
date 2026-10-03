// This file implements SSO as a bearer-token check: the SPA runs the OpenID
// Connect authorization-code-with-PKCE flow itself, as a public client (see
// frontend/src/auth/oidc.ts), and then sends the provider-issued access token
// (a JWT) as "Authorization: Bearer ..." on every /api/... request. This
// process never takes part in the provider's redirect flow, never sees an
// authorization code, and never holds a client secret — it only verifies
// each token it's handed (signature against the provider's JWKS, issuer,
// expiry, and that it was issued to the configured client) and resolves it
// to a local user, see requireUser in auth.go.

package webserver

import (
	"Tile-Server-Sync-GO/internal/config"
	"Tile-Server-Sync-GO/internal/configdb"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
)

// oidcTimeout bounds each network round-trip to the OIDC provider
// (discovery, JWKS refresh) so an unreachable or slow IdP fails the request
// instead of hanging it indefinitely.
const oidcTimeout = 10 * time.Second

// errSSODisabled is returned by ssoBearerUser when a bearer token arrives
// while SSO is switched off in the bootstrap file.
var errSSODisabled = errors.New("sso is not enabled")

// ssoVerifierCache holds one *oidc.IDTokenVerifier per issuer URL, built
// lazily on first use. Unlike the old redirect flow (which
// re-ran discovery on each, infrequent, interactive login), verification
// now happens on every API request, so the discovery document and JWKS must
// be cached: the verifier wraps go-oidc's RemoteKeySet, which keeps the
// provider's keys in memory and only refetches them when it sees an unknown
// key ID. The SSO config is fixed for the process's lifetime (it comes from
// the bootstrap file), so in practice this only ever holds one entry; keying
// by issuer just keeps it trivially correct. A failed discovery is not
// cached, so an IdP that's down at startup is retried on the next request. The lock is held across
// discovery on purpose, so a burst of first requests triggers only one.
type ssoVerifierCache struct {
	mu        sync.Mutex
	verifiers map[string]*oidc.IDTokenVerifier
}

func newSSOVerifierCache() *ssoVerifierCache {
	return &ssoVerifierCache{verifiers: map[string]*oidc.IDTokenVerifier{}}
}

func (c *ssoVerifierCache) get(ctx context.Context, issuerURL string) (*oidc.IDTokenVerifier, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if v, ok := c.verifiers[issuerURL]; ok {
		return v, nil
	}

	// The http.Client (not just ctx's deadline) carries the timeout: go-oidc
	// keeps this context, minus its cancellation, for every later background
	// JWKS refresh.
	client := &http.Client{Timeout: oidcTimeout}
	discoverCtx, cancel := context.WithTimeout(oidc.ClientContext(ctx, client), oidcTimeout)

	defer cancel()

	provider, err := oidc.NewProvider(discoverCtx, issuerURL)
	if err != nil {
		return nil, fmt.Errorf("discover oidc provider: %w", err)
	}

	// SkipClientIDCheck: an access token's "aud" is provider-specific (e.g.
	// Keycloak's default is "account", not the client ID), so go-oidc's
	// audience check would reject perfectly valid tokens. ssoBearerUser
	// binds the token to the configured client itself instead (see
	// tokenIssuedTo).
	v := provider.Verifier(&oidc.Config{SkipClientIDCheck: true})
	c.verifiers[issuerURL] = v

	return v, nil
}

// bearerClaims are the access-token claims ssoBearerUser reads. Audience is
// a []string-or-string in the JWT spec; go-oidc normalizes it onto
// oidc.IDToken.Audience, so only azp needs decoding here.
type bearerClaims struct {
	AuthorizedParty   string `json:"azp"`
	Email             string `json:"email"`
	PreferredUsername string `json:"preferred_username"`
}

// ssoBearerUser verifies rawToken against the bootstrap file's SSO config
// and resolves it to a local user, auto-provisioning one on first sight (see
// configdb.Store.FindOrCreateSSOUser). Called on every bearer-authenticated
// request, so a permission change made on /users takes effect on the very
// next request. The returned user's Permissions and IsSuperuser also include
// whatever the token's groups grant via oidc.groupPermissions (see
// groupGrants); that addition is never written back to the database.
func ssoBearerUser(
	ctx context.Context, cfgDB *configdb.Store, ssoCfg config.SSO, cache *ssoVerifierCache, rawToken string,
) (*configdb.User, error) {
	if !ssoCfg.Enabled {
		return nil, errSSODisabled
	}

	verifier, err := cache.get(ctx, ssoCfg.IssuerURL)
	if err != nil {
		return nil, err
	}

	token, err := verifier.Verify(ctx, rawToken)
	if err != nil {
		return nil, fmt.Errorf("verify token: %w", err)
	}

	var claims bearerClaims
	if err := token.Claims(&claims); err != nil {
		return nil, fmt.Errorf("decode claims: %w", err)
	}

	if !tokenIssuedTo(ssoCfg.ClientID, claims.AuthorizedParty, token.Audience) {
		return nil, errors.New("token was not issued to the configured client")
	}

	username := usernameFromClaims(claims.Email, claims.PreferredUsername, token.Subject)

	user, err := cfgDB.FindOrCreateSSOUser(
		ctx, token.Issuer, token.Subject, username, ssoDefaultPermissions(ssoCfg.DefaultPermissions),
	)
	if err != nil {
		return nil, fmt.Errorf("resolve sso user: %w", err)
	}

	if len(ssoCfg.GroupPermissions) > 0 {
		var all map[string]json.RawMessage
		if err := token.Claims(&all); err != nil {
			return nil, fmt.Errorf("decode claims: %w", err)
		}

		perms, superuser := groupGrants(ssoCfg.GroupPermissions, groupsFromClaim(all[ssoCfg.GroupsClaim]))
		user.Permissions = unionPermissions(user.Permissions, perms)
		user.IsSuperuser = user.IsSuperuser || superuser
	}

	return user, nil
}

// groupsFromClaim decodes a groups claim, which providers send either as a
// string array or, for a single group, as a plain string. A missing or
// differently shaped claim means no groups.
func groupsFromClaim(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}

	var groups []string
	if err := json.Unmarshal(raw, &groups); err == nil {
		return groups
	}

	var group string
	if err := json.Unmarshal(raw, &group); err == nil && group != "" {
		return []string{group}
	}

	return nil
}

// groupGrants is the union of what every group in groups is mapped to: the
// permissions, and whether any of them grants superuser. Groups without an
// entry grant nothing.
func groupGrants(mapping map[string]config.SSOGroupGrant, groups []string) (configdb.Permissions, bool) {
	var (
		granted   configdb.Permissions
		superuser bool
	)

	for _, g := range groups {
		if grant, ok := mapping[g]; ok {
			granted = unionPermissions(granted, ssoDefaultPermissions(grant.SSOPermissions))
			superuser = superuser || grant.Superuser
		}
	}

	return granted, superuser
}

// unionPermissions grants every permission either a or b grants.
func unionPermissions(a, b configdb.Permissions) configdb.Permissions {
	return configdb.Permissions{
		ViewStatus:         a.ViewStatus || b.ViewStatus,
		TriggerSync:        a.TriggerSync || b.TriggerSync,
		ViewConfig:         a.ViewConfig || b.ViewConfig,
		EditConfigAPI:      a.EditConfigAPI || b.EditConfigAPI,
		EditConfigDatabase: a.EditConfigDatabase || b.EditConfigDatabase,
		EditConfigMaps:     a.EditConfigMaps || b.EditConfigMaps,
	}
}

// tokenIssuedTo reports whether a token belongs to clientID: either its
// authorized party (azp — the client the token was issued to) or its
// audience names it. Without this, any token the same issuer minted for an
// unrelated client would be accepted here too.
func tokenIssuedTo(clientID, azp string, audience []string) bool {
	return azp == clientID || slices.Contains(audience, clientID)
}

// isTokenExpired reports whether err is (or wraps) go-oidc's expiry error —
// the one bearer failure that's routine (an idle browser tab) rather than
// security-relevant, so requireUser keeps it out of the security log.
func isTokenExpired(err error) bool {
	var expired *oidc.TokenExpiredError
	return errors.As(err, &expired)
}

// usernameFromClaims picks the local username a newly provisioned SSO
// account is created with (see configdb.Store.FindOrCreateSSOUser step 2:
// this is also what an admin should pre-create a placeholder account as, to
// have it linked instead of auto-provisioned): the email claim if present,
// else preferred_username, else the subject identifier itself.
func usernameFromClaims(email, preferredUsername, subject string) string {
	switch {
	case preferredUsername != "":
		return preferredUsername
	case email != "":
		return email
	default:
		return subject
	}
}
