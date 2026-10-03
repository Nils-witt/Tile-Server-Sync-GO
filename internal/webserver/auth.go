package webserver

import (
	"Tile-Server-Sync-GO/internal/config"
	"Tile-Server-Sync-GO/internal/configdb"
	"context"
	"fmt"
	"log"
	"net/http"
	"strings"
)

type contextKey int

const userContextKey contextKey = iota

// principal is the signed-in user of one request, built from its SSO access
// token alone (see ssoBearerUser): nothing about users is stored locally, and
// Permissions are oidc.defaultPermissions plus what the token's groups grant
// via oidc.groupPermissions; IsSuperuser comes from those groups alone. IsSuperuser is orthogonal to Permissions: it only
// gates the security log, and is not implied by, nor implies, any of the six
// feature permissions.
type principal struct {
	Username    string
	IsSuperuser bool
	Permissions config.SSOPermissions
}

// currentUser returns the principal attached to ctx by requireUser, if any.
func currentUser(ctx context.Context) (*principal, bool) {
	u, ok := ctx.Value(userContextKey).(*principal)
	return u, ok
}

// authenticator bundles what requireUser needs to resolve a request to a
// principal: the configdb store (for the security log), the bootstrap
// file's SSO config, and the cache of OIDC verifiers for bearer tokens (see
// sso_bearer.go). One is built per server in New and shared by every route.
type authenticator struct {
	cfgDB     *configdb.Store
	sso       config.SSO
	verifiers *ssoVerifierCache
}

// requireUser resolves the request to a principal before calling next,
// storing it in the request context (see currentUser). The only accepted
// credential is an "Authorization: Bearer" SSO access token (see
// ssoBearerUser) — there are no local accounts or session cookies.
// Every route in this package is JSON-only (the frontend is a client-routed
// SPA — see spa.go — with no server-rendered page left to redirect), so a
// missing/invalid credential always gets a 401 JSON body; the SPA itself
// decides whether to navigate to /login based on that.
func requireUser(a *authenticator) func(http.HandlerFunc) http.HandlerFunc {
	return func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			raw, ok := bearerToken(r)
			if !ok {
				writeJSON(w, http.StatusUnauthorized, errorJSON("not logged in"))
				return
			}

			user, err := ssoBearerUser(r.Context(), a.sso, a.verifiers, raw)
			if err != nil {
				rejectBearer(w, r, a.cfgDB, err)
				return
			}

			next(w, r.WithContext(context.WithValue(r.Context(), userContextKey, user)))
		}
	}
}

// bearerToken extracts the token from an "Authorization: Bearer <token>"
// header (scheme matched case-insensitively, per RFC 7235).
func bearerToken(r *http.Request) (string, bool) {
	scheme, token, ok := strings.Cut(r.Header.Get("Authorization"), " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") || strings.TrimSpace(token) == "" {
		return "", false
	}

	return strings.TrimSpace(token), true
}

// rejectBearer answers a failed bearer check with a generic 401 — the
// specific reason is only logged server-side. An expired token (a routine
// event for an idle tab, which the SPA recovers from by renewing it) only
// goes to stderr; anything else (bad signature, wrong issuer/client, SSO
// disabled) is also recorded as sso_login_failed in the security log.
func rejectBearer(w http.ResponseWriter, r *http.Request, cfgDB *configdb.Store, err error) {
	log.Printf("sso: bearer token rejected: %v", err)

	if !isTokenExpired(err) {
		logSecurityEvent(r, cfgDB, "sso_login_failed", "", err.Error())
	}

	writeJSON(w, http.StatusUnauthorized, errorJSON("invalid or expired token"))
}

// requirePermission composes requireUser with a check of the logged-in
// user's Permissions, denying with a 403 JSON body if check returns false.
func requirePermission(
	a *authenticator, check func(config.SSOPermissions) bool,
) func(http.HandlerFunc) http.HandlerFunc {
	return func(next http.HandlerFunc) http.HandlerFunc {
		return requireUser(a)(func(w http.ResponseWriter, r *http.Request) {
			user, _ := currentUser(r.Context())
			if !check(user.Permissions) {
				writeJSON(w, http.StatusForbidden, errorJSON("forbidden"))
				return
			}

			next(w, r)
		})
	}
}

// requireSuperuser composes requireUser with an IsSuperuser check, the same
// way requirePermission checks a Permissions flag.
func requireSuperuser(a *authenticator) func(http.HandlerFunc) http.HandlerFunc {
	return func(next http.HandlerFunc) http.HandlerFunc {
		return requireUser(a)(func(w http.ResponseWriter, r *http.Request) {
			user, _ := currentUser(r.Context())
			if !user.IsSuperuser {
				writeJSON(w, http.StatusForbidden, errorJSON("forbidden"))
				return
			}

			next(w, r)
		})
	}
}

// meResponse is what GET /api/me returns: enough for the SPA to decide what
// to show/hide for the signed-in user.
type meResponse struct {
	Username    string                `json:"username"`
	IsSuperuser bool                  `json:"isSuperuser"`
	Permissions config.SSOPermissions `json:"permissions"`
}

// meAPIHandler serves GET /api/me. It doubles as the audit point for SSO
// logins: a request carries no login step of its own on this server (the
// SPA talks to the provider directly), and logging every request would flood the security log, so sso_login is recorded
// here instead — the SPA calls /api/me exactly once right after completing
// the provider's login, and once per page load.
func meAPIHandler(cfgDB *configdb.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, ok := currentUser(r.Context())
		if !ok {
			writeJSON(w, http.StatusUnauthorized, errorJSON("not logged in"))
			return
		}

		logSecurityEvent(r, cfgDB, "sso_login", user.Username, fmt.Sprintf("isSuperuser=%v; permissions=%s",
			user.IsSuperuser, strings.Join(grantedPermissions(user.Permissions), ",")))

		writeJSON(w, http.StatusOK, meResponse{
			Username: user.Username, IsSuperuser: user.IsSuperuser, Permissions: user.Permissions,
		})
	}
}
