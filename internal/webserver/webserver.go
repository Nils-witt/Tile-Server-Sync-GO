// Package webserver exposes a JSON API (status, config, maps,
// security log, auth) consumed by the frontend package's embedded React SPA
// (see spa.go), backed by an internal/status.Recorder.
package webserver

import (
	"context"
	"net/http"
	"time"

	"github.com/Nils-witt/Tile-Server-Sync-GO/internal/config"
	"github.com/Nils-witt/Tile-Server-Sync-GO/internal/configdb"
	"github.com/Nils-witt/Tile-Server-Sync-GO/internal/status"
)

// Engine is what the web server needs from the sync engine (implemented by
// internal/syncer's *Engine): applying a saved config live, triggering a
// single map's sync, and the per-map cleanup/overlay side effects of map
// CRUD. Declared here, at the consumer, so this package doesn't depend on
// internal/syncer.
type Engine interface {
	Reload(ctx context.Context) error
	SyncMap(ctx context.Context, mapID string) (int, error)
	DeleteMapObjects(ctx context.Context, mapID string) (int64, error)
	CreateMapOverlays(ctx context.Context, m config.MapTarget) error
	UpdateMapOverlays(ctx context.Context, before, after config.MapTarget) error
	DeleteMapOverlays(ctx context.Context, m config.MapTarget) error
}

// Options configures New.
type Options struct {
	Addr     string
	Recorder *status.Recorder
	ConfigDB *configdb.Store
	SSO      config.SSO
	Version  string
	Commit   string
	Engine   Engine
}

// New builds an *http.Server serving the built React SPA (see spa.go) for
// every browser-navigated route ("/", "/config", "/security-log",
// "/login", and any client-side sub-route of those), backed by a
// JSON API under "/api/...": status (api/status), a config editor
// (per-section GET/PUT endpoints under api/config, plus the api/maps CRUD
// family for the Maps tab — see config.go and maps.go), and a
// superuser-only audit trail (api/security-log, see security_log.go)
// recording logins and config/map changes. Every API route is gated behind
// an SSO bearer token (see auth.go) and the permissions its groups grant
// (see config.SSOPermissions and oidc.groupPermissions); the SPA itself
// decides what to render based on GET /api/me and each request's own
// 401/403. It does not start
// listening; call ListenAndServe (typically in a goroutine).
//
// A successful config save also calls Engine.Reload itself, so the running process
// picks up the change immediately without a separate action — see
// finishConfigSave in config.go. The bootstrap-file-sourced webServer
// settings are deliberately not exposed or editable here, since applying a
// changed webServer.address needs a process restart the server
// itself can't safely trigger mid-request.
func New(opts Options) *http.Server {
	addr, rec, cfgDB, sso := opts.Addr, opts.Recorder, opts.ConfigDB, opts.SSO
	version, commit, eng := opts.Version, opts.Commit, opts.Engine
	reload := eng.Reload

	mux := http.NewServeMux()
	auth := &authenticator{cfgDB: cfgDB, sso: sso, verifiers: newSSOVerifierCache()}

	mux.HandleFunc("GET /api/me", requireUser(auth)(meAPIHandler()))
	mux.HandleFunc("POST /api/sso/login", requireUser(auth)(ssoLoginAPIHandler(cfgDB)))
	mux.HandleFunc("GET /api/version", versionAPIHandler(version, commit))

	// Unauthenticated: the login page needs it before any
	// credential exists, to decide whether (and how) to start the SPA's own
	// OIDC flow — see sso.go and sso_bearer.go. There is no server-side SSO
	// login route at all: the SPA talks to the provider directly and then
	// authenticates every API call with the provider's access token.
	mux.HandleFunc("GET /api/sso/status", ssoStatusAPIHandler(sso))

	mux.HandleFunc("GET /api/status", requirePermission(auth, permViewStatus)(statusAPIHandler(rec)))

	// Config: the api/database sections (the Maps tab is served by the
	// /api/maps family below instead). Each section has its own GET
	// (view_config) and PUT
	// (edit_config_{api,database}) registered separately, so the
	// permission each method requires is visible right here rather than
	// buried in a per-handler method switch.
	mux.HandleFunc("GET /api/config/api", requirePermission(auth, permViewConfig)(getAPISectionHandler(cfgDB)))
	mux.HandleFunc("PUT /api/config/api",
		requirePermission(auth, permEditConfigAPI)(saveAPISectionHandler(cfgDB, reload)))
	mux.HandleFunc("GET /api/config/database",
		requirePermission(auth, permViewConfig)(getDatabaseSectionHandler(cfgDB)))
	mux.HandleFunc("PUT /api/config/database",
		requirePermission(auth, permEditConfigDatabase)(saveDatabaseSectionHandler(cfgDB, reload)))

	// Maps: a first-class CRUD resource (see maps.go), not a config section —
	// each map is independently addressable/mutable, so adding or editing one
	// map no longer requires resubmitting every other configured map.
	mux.HandleFunc("GET /api/maps", requirePermission(auth, permViewConfig)(listMapsAPIHandler(cfgDB)))
	mux.HandleFunc("POST /api/maps",
		requirePermission(auth, permEditConfigMaps)(createMapAPIHandler(cfgDB, reload, eng.CreateMapOverlays)))
	mux.HandleFunc("PUT /api/maps/{id}",
		requirePermission(auth, permEditConfigMaps)(updateMapAPIHandler(cfgDB, reload, eng.UpdateMapOverlays)))
	mux.HandleFunc("DELETE /api/maps/{id}",
		requirePermission(auth, permEditConfigMaps)(
			deleteMapAPIHandler(cfgDB, reload, eng.DeleteMapObjects, eng.DeleteMapOverlays),
		))
	mux.HandleFunc("POST /api/maps/{id}/sync",
		requirePermission(auth, permTriggerSync)(syncMapAPIHandler(eng.SyncMap)))

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
