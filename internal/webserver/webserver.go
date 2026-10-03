// Package webserver exposes a JSON API (status, config, maps,
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
// every browser-navigated route ("/", "/config", "/security-log",
// "/login", and any client-side sub-route of those), backed by a
// JSON API under "/api/...": status (api/status), a config editor
// (api/config and its per-section GET/PUT endpoints, plus the api/maps CRUD
// family for the Maps tab — see config.go and maps.go), and a
// superuser-only audit trail (api/security-log, see security_log.go)
// recording logins and config/map changes. Every API route is gated behind
// an SSO bearer token (see auth.go) and the permissions its groups grant
// (see config.SSOPermissions and oidc.groupPermissions); the SPA itself
// decides what to render based on GET /api/me and each request's own
// 401/403. It does not start
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
	addr string, rec *status.Recorder, cfgDB *configdb.Store, webServer config.WebServer, sso config.SSO,
	version, commit string,
	reload func(context.Context) error, syncMap func(context.Context, string) (int, error),
	deleteMapObjects func(context.Context, string) (int64, error),
	createMapOverlays func(context.Context, config.MapTarget) error,
	updateMapOverlays func(context.Context, config.MapTarget, config.MapTarget) error,
	deleteMapOverlays func(context.Context, config.MapTarget) error,
) *http.Server {
	mux := http.NewServeMux()
	auth := &authenticator{cfgDB: cfgDB, sso: sso, verifiers: newSSOVerifierCache()}

	mux.HandleFunc("GET /api/me", requireUser(auth)(meAPIHandler(cfgDB)))
	mux.HandleFunc("GET /api/version", versionAPIHandler(version, commit))

	// Unauthenticated: the login page needs it before any
	// credential exists, to decide whether (and how) to start the SPA's own
	// OIDC flow — see sso.go and sso_bearer.go. There is no server-side SSO
	// login route at all: the SPA talks to the provider directly and then
	// authenticates every API call with the provider's access token.
	mux.HandleFunc("GET /api/sso/status", ssoStatusAPIHandler(sso))

	mux.HandleFunc("GET /api/status", requirePermission(auth, permViewStatus)(statusAPIHandler(rec)))

	// Config: GET /api/config is the whole-config bundle (api/database
	// sections — the Maps tab is served by the /api/maps family below
	// instead). Each section has its own GET (view_config) and PUT
	// (edit_config_{api,database}) registered separately, so the
	// permission each method requires is visible right here rather than
	// buried in a per-handler method switch.
	mux.HandleFunc("GET /api/config", requirePermission(auth, permViewConfig)(configAPIHandler(cfgDB, webServer)))
	mux.HandleFunc("GET /api/config/api", requirePermission(auth, permViewConfig)(getAPISectionHandler(cfgDB)))
	mux.HandleFunc("PUT /api/config/api",
		requirePermission(auth, permEditConfigAPI)(saveAPISectionHandler(cfgDB, webServer, reload)))
	mux.HandleFunc("GET /api/config/database",
		requirePermission(auth, permViewConfig)(getDatabaseSectionHandler(cfgDB)))
	mux.HandleFunc("PUT /api/config/database",
		requirePermission(auth, permEditConfigDatabase)(saveDatabaseSectionHandler(cfgDB, webServer, reload)))

	// Maps: a first-class CRUD resource (see maps.go), not a config section —
	// each map is independently addressable/mutable, so adding or editing one
	// map no longer requires resubmitting every other configured map.
	mux.HandleFunc("GET /api/maps", requirePermission(auth, permViewConfig)(listMapsAPIHandler(cfgDB)))
	mux.HandleFunc("POST /api/maps",
		requirePermission(auth, permEditConfigMaps)(createMapAPIHandler(cfgDB, reload, createMapOverlays)))
	mux.HandleFunc("GET /api/maps/{id}", requirePermission(auth, permViewConfig)(getMapAPIHandler(cfgDB)))
	mux.HandleFunc("PUT /api/maps/{id}",
		requirePermission(auth, permEditConfigMaps)(updateMapAPIHandler(cfgDB, reload, updateMapOverlays)))
	mux.HandleFunc("DELETE /api/maps/{id}",
		requirePermission(auth, permEditConfigMaps)(
			deleteMapAPIHandler(cfgDB, reload, deleteMapObjects, deleteMapOverlays),
		))
	mux.HandleFunc("POST /api/maps/{id}/sync",
		requirePermission(auth, permTriggerSync)(syncMapAPIHandler(syncMap)))

	mux.HandleFunc("GET /api/security-log", requireSuperuser(auth)(securityLogAPIHandler(cfgDB)))

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

// permViewStatus and friends adapt config.SSOPermissions' fields to the
// func(config.SSOPermissions) bool shape requirePermission expects.
func permViewStatus(p config.SSOPermissions) bool    { return p.ViewStatus }
func permTriggerSync(p config.SSOPermissions) bool   { return p.TriggerSync }
func permViewConfig(p config.SSOPermissions) bool    { return p.ViewConfig }
func permEditConfigAPI(p config.SSOPermissions) bool { return p.EditConfigAPI }

func permEditConfigDatabase(p config.SSOPermissions) bool { return p.EditConfigDatabase }
func permEditConfigMaps(p config.SSOPermissions) bool     { return p.EditConfigMaps }
