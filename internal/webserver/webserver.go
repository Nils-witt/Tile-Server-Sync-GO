// Package webserver exposes a JSON API (status, config, maps, users,
// security log, auth) consumed by the frontend package's embedded React SPA
// (see spa.go), backed by an internal/status.Recorder.
package webserver

import (
	"Tile-Server-Sync-GO/internal/config"
	"Tile-Server-Sync-GO/internal/configdb"
	"Tile-Server-Sync-GO/internal/status"
	"context"
	"net/http"
	"time"
)

// New builds an *http.Server serving the built React SPA (see spa.go) for
// every browser-navigated route ("/", "/config", "/users", "/security-log",
// "/login", "/setup", and any client-side sub-route of those), backed by a
// JSON API under "/api/...": status (api/status), a config editor
// (api/config and its per-section GET/PUT endpoints, plus the api/maps CRUD
// family for the Maps tab — see config.go and maps.go), user management
// (api/users), and a superuser-only audit trail (api/security-log, see
// security_log.go) recording logins, logouts, user-account changes, and
// config saves. Every API route is gated behind a session-cookie login (see
// auth.go) and the logged-in user's permissions (see configdb.Permissions);
// the SPA itself decides what to render based on GET /api/me, GET
// /api/setup-status, and each request's own 401/403. It does not start
// listening; call ListenAndServe (typically in a goroutine).
//
// A successful config save also calls reload itself, so the running process
// picks up the change immediately without a separate action — see
// finishConfigSave in config.go. webServer is the fixed,
// bootstrap-file-sourced WebServer value: the config editor always displays
// it for context but can never change it, since applying a changed
// webServer.enabled/address needs a process restart the server itself can't
// safely trigger mid-request.
func New(
	addr string, rec *status.Recorder, cfgDB *configdb.Store, webServer config.WebServer,
	version, commit string,
	reload func(context.Context) error, syncMap func(context.Context, string) (int, error),
	deleteMapObjects func(context.Context, string) (int64, error),
	createMapOverlays func(context.Context, config.MapTarget) error,
	updateMapOverlays func(context.Context, config.MapTarget, config.MapTarget) error,
	deleteMapOverlays func(context.Context, config.MapTarget) error,
) *http.Server {
	mux := http.NewServeMux()

	mux.HandleFunc("POST /api/setup", setupHandler(cfgDB))
	mux.HandleFunc("GET /api/setup-status", setupStatusAPIHandler(cfgDB))
	mux.HandleFunc("POST /api/login", loginHandler(cfgDB))
	mux.HandleFunc("POST /api/logout", logoutHandler(cfgDB))
	mux.HandleFunc("GET /api/me", requireUser(cfgDB)(meAPIHandler))
	mux.HandleFunc("GET /api/version", versionAPIHandler(version, commit))

	// SSO login-flow routes are deliberately unauthenticated (like
	// /api/login itself): a session doesn't exist yet at the point these are
	// reached. Unlike every other route here, they're real browser
	// navigations/redirects (the OAuth dance), not fetch calls, so they stay
	// outside /api/... — the SPA just sets window.location to them. Both
	// handlers already 405 any non-GET method themselves, but they must
	// still be *registered* as "GET ..." (not a bare, all-methods pattern):
	// net/http's ServeMux refuses to register a bare "/login/sso" alongside
	// the catch-all "GET /" registered below for the SPA shell, since
	// neither pattern would then dominate the other on both the method and
	// path dimensions (a bare pattern matches every method but this one
	// exact path; "GET /" matches only GET but every path) — an ambiguity
	// ServeMux rejects at registration time (panics) rather than resolves.
	mux.HandleFunc("GET /api/sso/status", ssoStatusAPIHandler(cfgDB))
	mux.HandleFunc("GET /login/sso", loginSSOStartHandler(cfgDB))
	mux.HandleFunc("GET /login/sso/callback", loginSSOCallbackHandler(cfgDB))

	mux.HandleFunc("GET /api/status", requirePermission(cfgDB, permViewStatus)(statusAPIHandler(rec)))

	// Config: GET /api/config is the whole-config bundle (api/database
	// sections — the Maps tab is served by the /api/maps family below
	// instead). Each section has its own GET (view_config) and PUT
	// (edit_config_{api,database,sso}) registered separately, so the
	// permission each method requires is visible right here rather than
	// buried in a per-handler method switch.
	mux.HandleFunc("GET /api/config", requirePermission(cfgDB, permViewConfig)(configAPIHandler(cfgDB, webServer)))
	mux.HandleFunc("GET /api/config/api", requirePermission(cfgDB, permViewConfig)(getAPISectionHandler(cfgDB)))
	mux.HandleFunc("PUT /api/config/api",
		requirePermission(cfgDB, permEditConfigAPI)(saveAPISectionHandler(cfgDB, webServer, reload)))
	mux.HandleFunc("GET /api/config/database",
		requirePermission(cfgDB, permViewConfig)(getDatabaseSectionHandler(cfgDB)))
	mux.HandleFunc("PUT /api/config/database",
		requirePermission(cfgDB, permEditConfigDatabase)(saveDatabaseSectionHandler(cfgDB, webServer, reload)))
	mux.HandleFunc("GET /api/config/sso", requirePermission(cfgDB, permViewConfig)(getSSOConfigHandler(cfgDB)))
	mux.HandleFunc("PUT /api/config/sso",
		requirePermission(cfgDB, permEditConfigSSO)(saveSSOConfigHandler(cfgDB)))

	// Maps: a first-class CRUD resource (see maps.go), not a config section —
	// each map is independently addressable/mutable, so adding or editing one
	// map no longer requires resubmitting every other configured map.
	mux.HandleFunc("GET /api/maps", requirePermission(cfgDB, permViewConfig)(listMapsAPIHandler(cfgDB)))
	mux.HandleFunc("POST /api/maps",
		requirePermission(cfgDB, permEditConfigMaps)(createMapAPIHandler(cfgDB, reload, createMapOverlays)))
	mux.HandleFunc("GET /api/maps/{id}", requirePermission(cfgDB, permViewConfig)(getMapAPIHandler(cfgDB)))
	mux.HandleFunc("PUT /api/maps/{id}",
		requirePermission(cfgDB, permEditConfigMaps)(updateMapAPIHandler(cfgDB, reload, updateMapOverlays)))
	mux.HandleFunc("DELETE /api/maps/{id}",
		requirePermission(cfgDB, permEditConfigMaps)(
			deleteMapAPIHandler(cfgDB, reload, deleteMapObjects, deleteMapOverlays),
		))
	mux.HandleFunc("POST /api/maps/{id}/sync",
		requirePermission(cfgDB, permTriggerSync)(syncMapAPIHandler(syncMap)))

	mux.HandleFunc("GET /api/users", requireSuperuser(cfgDB)(listUsersAPIHandler(cfgDB)))
	mux.HandleFunc("POST /api/users", requireSuperuser(cfgDB)(createUserAPIHandler(cfgDB)))
	mux.HandleFunc("GET /api/users/{id}", requireSuperuser(cfgDB)(getUserAPIHandler(cfgDB)))
	mux.HandleFunc("PUT /api/users/{id}", requireSuperuser(cfgDB)(updateUserAPIHandler(cfgDB)))
	mux.HandleFunc("PATCH /api/users/{id}", requireSuperuser(cfgDB)(updateUserAPIHandler(cfgDB)))
	mux.HandleFunc("DELETE /api/users/{id}", requireSuperuser(cfgDB)(deleteUserAPIHandler(cfgDB)))
	mux.HandleFunc("GET /api/security-log", requireSuperuser(cfgDB)(securityLogAPIHandler(cfgDB)))

	// The SPA shell: registered last (net/http's ServeMux resolves by
	// pattern specificity regardless of registration order, but the ordering
	// here mirrors "API routes first, catch-all fallback last" for
	// readability). "GET /" is a method-qualified subtree wildcard, not a
	// bare "/" — a bare "/" would match every unmatched *method* too on
	// every path, which would silently suppress net/http's automatic 405
	// Method Not Allowed handling for every "/api/..." route above (a wrong
	// verb on a registered API path falling through to the SPA shell instead
	// of 405ing). "GET /" only ever competes with a GET request, and every
	// "/api/..." pattern above is strictly more specific than it for the
	// paths it actually owns, so those still win for GET too.
	dist, err := spaFS()
	if err != nil {
		panic("webserver: load embedded frontend build: " + err.Error())
	}

	mux.HandleFunc("GET /", spaHandler(dist))

	return &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
}

// permViewStatus and friends adapt configdb.Permissions' fields to the
// func(configdb.Permissions) bool shape requirePermission expects.
func permViewStatus(p configdb.Permissions) bool    { return p.ViewStatus }
func permTriggerSync(p configdb.Permissions) bool   { return p.TriggerSync }
func permViewConfig(p configdb.Permissions) bool    { return p.ViewConfig }
func permEditConfigAPI(p configdb.Permissions) bool { return p.EditConfigAPI }

func permEditConfigDatabase(p configdb.Permissions) bool { return p.EditConfigDatabase }
func permEditConfigMaps(p configdb.Permissions) bool     { return p.EditConfigMaps }
func permEditConfigSSO(p configdb.Permissions) bool      { return p.EditConfigSSO }
