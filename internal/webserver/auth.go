package webserver

import (
	"Tile-Server-Sync-GO/internal/configdb"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
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

const userContextKey contextKey = 0

// currentUser returns the user attached to ctx by requireUser, if any.
func currentUser(ctx context.Context) (*configdb.User, bool) {
	u, ok := ctx.Value(userContextKey).(*configdb.User)
	return u, ok
}

// setupGate wraps the whole mux: while no account exists yet, every request
// except /setup itself is redirected there; once at least one account
// exists, /setup redirects to /login instead of ever rendering again. A
// UserCount error fails toward "proceed to normal auth" (requireUser will
// then simply reject the missing/invalid session) rather than either
// bypassing setup or blocking the entire server on a transient DB hiccup.
func setupGate(cfgDB *configdb.Store, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n, err := cfgDB.UserCount(r.Context())
		if err != nil {
			next.ServeHTTP(w, r)
			return
		}

		switch {
		case n == 0 && r.URL.Path != "/setup":
			http.Redirect(w, r, "/setup", http.StatusSeeOther)
		case n > 0 && r.URL.Path == "/setup":
			http.Redirect(w, r, "/login", http.StatusSeeOther)
		default:
			next.ServeHTTP(w, r)
		}
	})
}

// requireUser resolves the session cookie to a user before calling next,
// storing the user in the request context (see currentUser). page controls
// how a missing/invalid session is reported: true redirects to
// /login?next=<original path> (for browser-rendered pages), false writes a
// 401 JSON body (for the fetch-driven JSON API).
func requireUser(cfgDB *configdb.Store, page bool) func(http.HandlerFunc) http.HandlerFunc {
	return func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			cookie, err := r.Cookie(sessionCookieName)
			if err != nil {
				denyUnauthenticated(w, r, page)
				return
			}

			user, err := cfgDB.SessionUser(r.Context(), cookie.Value)
			if err != nil {
				denyUnauthenticated(w, r, page)
				return
			}

			next(w, r.WithContext(context.WithValue(r.Context(), userContextKey, user)))
		}
	}
}

func denyUnauthenticated(w http.ResponseWriter, r *http.Request, page bool) {
	if page {
		next := url.QueryEscape(r.URL.RequestURI())
		http.Redirect(w, r, "/login?next="+next, http.StatusSeeOther)

		return
	}

	writeJSON(w, http.StatusUnauthorized, errorJSON("not logged in"))
}

// requirePermission composes requireUser with a check of the logged-in
// user's Permissions, denying with a 403 (page: plain-text error, api: JSON)
// if check returns false.
func requirePermission(
	cfgDB *configdb.Store, page bool, check func(configdb.Permissions) bool,
) func(http.HandlerFunc) http.HandlerFunc {
	return func(next http.HandlerFunc) http.HandlerFunc {
		return requireUser(cfgDB, page)(func(w http.ResponseWriter, r *http.Request) {
			user, _ := currentUser(r.Context())
			if !check(user.Permissions) {
				denyForbidden(w, page)
				return
			}

			next(w, r)
		})
	}
}

// requireSuperuser composes requireUser with an IsSuperuser check, the same
// way requirePermission checks a Permissions flag.
func requireSuperuser(cfgDB *configdb.Store, page bool) func(http.HandlerFunc) http.HandlerFunc {
	return func(next http.HandlerFunc) http.HandlerFunc {
		return requireUser(cfgDB, page)(func(w http.ResponseWriter, r *http.Request) {
			user, _ := currentUser(r.Context())
			if !user.IsSuperuser {
				denyForbidden(w, page)
				return
			}

			next(w, r)
		})
	}
}

func denyForbidden(w http.ResponseWriter, page bool) {
	if page {
		http.Error(w, "forbidden: you don't have permission to view this page", http.StatusForbidden)
		return
	}

	writeJSON(w, http.StatusForbidden, errorJSON("forbidden"))
}

// loginRequest/loginResponse are the JSON shapes for POST /login when called
// via fetch; the login page also supports a plain HTML form post (no
// JavaScript required to log in).
type loginResponse struct {
	Error string `json:"error,omitempty"`
}

// pageGetPostHandler serves html on GET and delegates to handlePost on POST,
// 405-ing any other method — the shared shape behind both loginHandler and
// setupHandler.
func pageGetPostHandler(html string, handlePost http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(html))
		case http.MethodPost:
			handlePost(w, r)
		default:
			w.Header().Set("Allow", http.MethodGet+", "+http.MethodPost)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	}
}

func loginHandler(cfgDB *configdb.Store) http.HandlerFunc {
	return pageGetPostHandler(loginPageHTML, func(w http.ResponseWriter, r *http.Request) {
		handleLoginPost(w, r, cfgDB)
	})
}

func handleLoginPost(w http.ResponseWriter, r *http.Request, cfgDB *configdb.Store) {
	username, password, next, err := readLoginCredentials(w, r)
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
	writeJSON(w, http.StatusOK, map[string]string{"redirect": safeNext(next)})
}

// readLoginCredentials accepts either a JSON body ({username,password,next})
// or an HTML form post, so the login page works both with and without its
// own JavaScript.
func readLoginCredentials(w http.ResponseWriter, r *http.Request) (username, password, next string, err error) {
	if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		var req struct {
			Username string `json:"username"`
			Password string `json:"password"`
			Next     string `json:"next"`
		}

		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
			return "", "", "", errors.New("decode request: " + err.Error())
		}

		return req.Username, req.Password, req.Next, nil
	}

	if err := r.ParseForm(); err != nil {
		return "", "", "", errors.New("parse form: " + err.Error())
	}

	return r.FormValue("username"), r.FormValue("password"), r.FormValue("next"), nil
}

// safeNext returns next if it's a same-site relative path (starts with "/"
// but not "//", which browsers treat as protocol-relative and could send a
// logged-in user off-site), or "/" otherwise.
func safeNext(next string) string {
	if next == "" || !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") {
		return "/"
	}

	return next
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
		http.Redirect(w, r, "/login", http.StatusSeeOther)
	}
}

func setupHandler(cfgDB *configdb.Store) http.HandlerFunc {
	return pageGetPostHandler(setupPageHTML, func(w http.ResponseWriter, r *http.Request) {
		handleSetupPost(w, r, cfgDB)
	})
}

// allPermissions is what the very first account is created with: there's no
// one else yet to have granted anything more selectively, and the first
// account is always a superuser too (see setupHandler).
func allPermissions() configdb.Permissions {
	return configdb.Permissions{
		ViewStatus: true, TriggerSync: true, ViewConfig: true,
		EditConfigAPI: true, EditConfigDatabase: true, EditConfigMaps: true, EditConfigSSO: true,
	}
}

func handleSetupPost(w http.ResponseWriter, r *http.Request, cfgDB *configdb.Store) {
	// setupGate already ensures this handler only runs while the users table
	// is empty, but re-check here to close the race between two browsers
	// both loading /setup before either has submitted.
	if n, err := cfgDB.UserCount(r.Context()); err != nil || n > 0 {
		writeJSON(w, http.StatusConflict, loginResponse{Error: "setup already completed"})
		return
	}

	if err := r.ParseForm(); err != nil {
		writeJSON(w, http.StatusBadRequest, loginResponse{Error: "parse form: " + err.Error()})
		return
	}

	username := r.FormValue("username")
	password := r.FormValue("password")

	if username == "" || password == "" {
		writeJSON(w, http.StatusBadRequest, loginResponse{Error: "username and password are required"})
		return
	}

	user, err := cfgDB.CreateUser(r.Context(), username, password, allPermissions(), true)
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
	writeJSON(w, http.StatusOK, map[string]string{"redirect": "/"})
}

// meResponse is what GET /api/me returns: enough for every page's script to
// decide what to show/hide for the logged-in user.
type meResponse struct {
	Username    string               `json:"username"`
	IsSuperuser bool                 `json:"isSuperuser"`
	Permissions configdb.Permissions `json:"permissions"`
}

func meAPIHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)

		return
	}

	user, ok := currentUser(r.Context())
	if !ok {
		writeJSON(w, http.StatusUnauthorized, errorJSON("not logged in"))
		return
	}

	writeJSON(w, http.StatusOK, meResponse{
		Username: user.Username, IsSuperuser: user.IsSuperuser, Permissions: user.Permissions,
	})
}
