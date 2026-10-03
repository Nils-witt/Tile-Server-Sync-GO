package webserver

import (
	"Tile-Server-Sync-GO/internal/config"
	"Tile-Server-Sync-GO/internal/configdb"
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"
	"time"
)

// sessionCookieName is the cookie holding a session's raw token (see
// configdb.Store.CreateSession/SessionUser, which only ever store its hash).
const sessionCookieName = "gso_session"

// sessionTTL is how long a session stays valid after login, renewed by
// simply logging in again (there's no sliding-expiration refresh).
const sessionTTL = 7 * 24 * time.Hour

type contextKey int

const (
	userContextKey contextKey = iota
	bearerAuthContextKey
)

// currentUser returns the user attached to ctx by requireUser, if any.
func currentUser(ctx context.Context) (*configdb.User, bool) {
	u, ok := ctx.Value(userContextKey).(*configdb.User)
	return u, ok
}

// viaBearer reports whether requireUser authenticated this request with an
// SSO bearer token rather than a session cookie.
func viaBearer(ctx context.Context) bool {
	b, _ := ctx.Value(bearerAuthContextKey).(bool)
	return b
}

// authenticator bundles what requireUser needs to resolve a request to a
// user: the configdb store (sessions, users), the bootstrap file's SSO
// config, and the cache of OIDC verifiers for bearer tokens (see
// sso_bearer.go). One is built per server in New and shared by every route.
type authenticator struct {
	cfgDB     *configdb.Store
	sso       config.SSO
	verifiers *ssoVerifierCache
}

// requireUser resolves the request to a user before calling next, storing
// the user in the request context (see currentUser). Two credentials are
// accepted: an "Authorization: Bearer" SSO access token (see
// ssoBearerUser) — checked first, and with no fallback to the cookie if it
// fails, so a stale token is never silently masked by an unrelated session —
// or else the local-login session cookie. Every route in this package is
// JSON-only (the frontend is a client-routed SPA — see spa.go — with no
// server-rendered page left to redirect), so a missing/invalid credential
// always gets a 401 JSON body; the SPA itself decides whether to navigate
// to /login based on that.
func requireUser(a *authenticator) func(http.HandlerFunc) http.HandlerFunc {
	return func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if raw, ok := bearerToken(r); ok {
				user, err := ssoBearerUser(r.Context(), a.cfgDB, a.sso, a.verifiers, raw)
				if err != nil {
					rejectBearer(w, r, a.cfgDB, err)
					return
				}

				ctx := context.WithValue(r.Context(), userContextKey, user)
				next(w, r.WithContext(context.WithValue(ctx, bearerAuthContextKey, true)))

				return
			}

			cookie, err := r.Cookie(sessionCookieName)
			if err != nil {
				writeJSON(w, http.StatusUnauthorized, errorJSON("not logged in"))
				return
			}

			user, err := a.cfgDB.SessionUser(r.Context(), cookie.Value)
			if err != nil {
				writeJSON(w, http.StatusUnauthorized, errorJSON("not logged in"))
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
	a *authenticator, check func(configdb.Permissions) bool,
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

// loginResponse is the JSON shape for POST /api/login and POST /api/setup.
type loginResponse struct {
	Error string `json:"error,omitempty"`
}

// setupStatusResponse is what GET /api/setup-status returns: whether the
// SPA should route to /setup (no account exists yet) instead of /login.
type setupStatusResponse struct {
	NeedsSetup bool `json:"needsSetup"`
}

// setupStatusAPIHandler serves GET /api/setup-status, deliberately
// unauthenticated like /api/login and /api/sso/status — it's what the SPA
// calls before any session exists to decide whether to render /setup or
// /login. A UserCount error fails toward "setup not needed" (normal auth
// then simply rejects the missing/invalid session) rather than either
// bypassing setup or blocking the whole app on a transient DB hiccup.
func setupStatusAPIHandler(cfgDB *configdb.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)

			return
		}

		n, err := cfgDB.UserCount(r.Context())
		writeJSON(w, http.StatusOK, setupStatusResponse{NeedsSetup: err == nil && n == 0})
	}
}

func loginHandler(cfgDB *configdb.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)

			return
		}

		handleLoginPost(w, r, cfgDB)
	}
}

func handleLoginPost(w http.ResponseWriter, r *http.Request, cfgDB *configdb.Store) {
	username, password, err := readLoginCredentials(w, r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, loginResponse{Error: err.Error()})
		return
	}

	user, err := cfgDB.VerifyPassword(r.Context(), username, password)
	if err != nil {
		logSecurityEvent(r, cfgDB, "login_failed", username, "")
		writeJSON(w, http.StatusUnauthorized, loginResponse{Error: "invalid username or password"})

		return
	}

	token, expiresAt, err := cfgDB.CreateSession(r.Context(), user.ID, sessionTTL)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, loginResponse{Error: err.Error()})
		return
	}

	logSecurityEvent(r, cfgDB, "login", user.Username, "")
	setSessionCookie(w, r, token, expiresAt)
	writeJSON(w, http.StatusOK, meResponse{Username: user.Username, IsSuperuser: user.IsSuperuser, Permissions: user.Permissions})
}

// readLoginCredentials decodes a JSON {username,password} body — the SPA is
// the only caller, unlike the old server-rendered login page which also had
// to support a plain HTML form post for no-JavaScript use.
func readLoginCredentials(w http.ResponseWriter, r *http.Request) (username, password string, err error) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}

	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		return "", "", errors.New("decode request: " + err.Error())
	}

	return req.Username, req.Password, nil
}

// setSessionCookie and clearSessionCookie set Secure conditionally on
// r.TLS rather than unconditionally true: this server is documented (see
// webserver.go) as usable on a plain-HTTP trusted network, and a browser
// silently drops a Secure cookie set over plain HTTP, which would break
// login entirely in that deployment. HttpOnly and SameSite=Lax are always
// set regardless.
func setSessionCookie(w http.ResponseWriter, r *http.Request, token string, expiresAt time.Time) {
	http.SetCookie(w, &http.Cookie{ //nolint:gosec // Secure is conditional on r.TLS, see comment above
		Name:     sessionCookieName,
		Value:    token,
		Path:     "/",
		Expires:  expiresAt,
		HttpOnly: true,
		Secure:   r.TLS != nil,
		SameSite: http.SameSiteLaxMode,
	})
}

func clearSessionCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{ //nolint:gosec // Secure is conditional on r.TLS, see setSessionCookie
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		Expires:  time.Unix(0, 0),
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   r.TLS != nil,
		SameSite: http.SameSiteLaxMode,
	})
}

func logoutHandler(cfgDB *configdb.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)

			return
		}

		if cookie, err := r.Cookie(sessionCookieName); err == nil {
			if user, err := cfgDB.SessionUser(r.Context(), cookie.Value); err == nil {
				logSecurityEvent(r, cfgDB, "logout", user.Username, "")
			}

			_ = cfgDB.DeleteSession(r.Context(), cookie.Value)
		}

		clearSessionCookie(w, r)
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}
}

func setupHandler(cfgDB *configdb.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)

			return
		}

		handleSetupPost(w, r, cfgDB)
	}
}

// allPermissions is what the very first account is created with: there's no
// one else yet to have granted anything more selectively, and the first
// account is always a superuser too (see setupHandler).
func allPermissions() configdb.Permissions {
	return configdb.Permissions{
		ViewStatus: true, TriggerSync: true, ViewConfig: true,
		EditConfigAPI: true, EditConfigDatabase: true, EditConfigMaps: true,
	}
}

func handleSetupPost(w http.ResponseWriter, r *http.Request, cfgDB *configdb.Store) {
	// Re-checked here (not just relied on via GET /api/setup-status, which
	// the SPA uses only to decide which page to render) to close the race
	// between two browsers both loading /setup before either has submitted.
	if n, err := cfgDB.UserCount(r.Context()); err != nil || n > 0 {
		writeJSON(w, http.StatusConflict, loginResponse{Error: "setup already completed"})
		return
	}

	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}

	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, loginResponse{Error: "decode request: " + err.Error()})
		return
	}

	if req.Username == "" || req.Password == "" {
		writeJSON(w, http.StatusBadRequest, loginResponse{Error: "username and password are required"})
		return
	}

	user, err := cfgDB.CreateUser(r.Context(), req.Username, req.Password, allPermissions(), true)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, loginResponse{Error: err.Error()})
		return
	}

	logSecurityEvent(r, cfgDB, "user_created", user.Username, "initial setup account, superuser")

	token, expiresAt, err := cfgDB.CreateSession(r.Context(), user.ID, sessionTTL)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, loginResponse{Error: err.Error()})
		return
	}

	logSecurityEvent(r, cfgDB, "login", user.Username, "")
	setSessionCookie(w, r, token, expiresAt)
	writeJSON(w, http.StatusOK, meResponse{Username: user.Username, IsSuperuser: user.IsSuperuser, Permissions: user.Permissions})
}

// meResponse is what GET /api/me (and successful POST /api/login,
// /api/setup) return: enough for the SPA to decide what to show/hide for the
// logged-in user.
type meResponse struct {
	Username    string               `json:"username"`
	IsSuperuser bool                 `json:"isSuperuser"`
	Permissions configdb.Permissions `json:"permissions"`
}

// meAPIHandler serves GET /api/me. It doubles as the audit point for SSO
// logins: a bearer-authenticated request carries no login step of its own
// on this server (the SPA talks to the provider directly), and logging every
// bearer request would flood the security log, so sso_login is recorded
// here instead — the SPA calls /api/me exactly once right after completing
// the provider's login, and once per page load.
func meAPIHandler(cfgDB *configdb.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, ok := currentUser(r.Context())
		if !ok {
			writeJSON(w, http.StatusUnauthorized, errorJSON("not logged in"))
			return
		}

		if viaBearer(r.Context()) {
			logSecurityEvent(r, cfgDB, "sso_login", user.Username, "")
		}

		writeJSON(w, http.StatusOK, meResponse{
			Username: user.Username, IsSuperuser: user.IsSuperuser, Permissions: user.Permissions,
		})
	}
}
