// Package webserver exposes a minimal HTTP status page showing sync status
// and recent log output, backed by an internal/status.Recorder.
package webserver

import (
	"Tile-Server-Sync-GO/internal/config"
	"Tile-Server-Sync-GO/internal/configdb"
	"Tile-Server-Sync-GO/internal/status"
	"context"
	"html/template"
	"net/http"
	"strings"
	"time"
)

// New builds an *http.Server serving the status page at "/" (with a
// per-map "Sync" button hitting "POST /api/maps/{id}/sync"), a config editor
// at "/config" (backed by a JSON API at "/api/config" and its per-section
// GET/PUT endpoints, plus the "/api/maps" CRUD family for the Maps tab — see
// config.go and maps.go), a user management page at "/users", and a superuser-only
// audit trail at "/security-log" (backed by "/api/security-log", see
// security_log.go) recording logins, logouts, user-account changes, and
// config saves — all gated behind a session-cookie login (see auth.go) and
// the logged-in user's permissions (see configdb.Permissions). While no
// account exists yet, every request is
// redirected to a one-time "/setup" page (setupGate) that creates the first,
// fully-permissioned superuser account. It does not start listening; call
// ListenAndServe (typically in a goroutine).
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
	// version/commit (main.go's -ldflags-set build info) aren't known until
	// New() is called, so the shared copyright footer's {{FOOTER}} marker
	// (left in every page by renderPage/pageReplacer, see web.go) is
	// resolved here rather than at package init.
	footer := buildFooter(version, commit)

	loginPageHTML = strings.Replace(loginPageHTML, "{{FOOTER}}", footer, 1)
	setupPageHTML = strings.Replace(setupPageHTML, "{{FOOTER}}", footer, 1)
	configPageHTML = strings.Replace(configPageHTML, "{{FOOTER}}", footer, 1)
	usersPageHTML = strings.Replace(usersPageHTML, "{{FOOTER}}", footer, 1)
	securityLogPageHTML = strings.Replace(securityLogPageHTML, "{{FOOTER}}", footer, 1)

	// status.html needs its {{FOOTER}} resolved before html/template parses
	// it, not after: {{FOOTER}} would otherwise be parsed as an (invalid)
	// template action rather than left alone the way pageReplacer's own
	// tokens are (see renderPage/pageReplacer in web.go).
	statusHTML := strings.Replace(renderPage("status.html"), "{{FOOTER}}", footer, 1)
	pageTemplate := template.Must(template.New("status").Parse(statusHTML))

	mux := http.NewServeMux()

	mux.HandleFunc("/setup", setupHandler(cfgDB))
	mux.HandleFunc("/login", loginHandler(cfgDB))
	mux.HandleFunc("/logout", logoutHandler(cfgDB))
	mux.HandleFunc("GET /api/me", requireUser(cfgDB, false)(meAPIHandler))

	// SSO login-flow routes are deliberately unauthenticated (like /login
	// itself): a session doesn't exist yet at the point these are reached.
	mux.HandleFunc("GET /api/sso/status", ssoStatusAPIHandler(cfgDB))
	mux.HandleFunc("/login/sso", loginSSOStartHandler(cfgDB))
	mux.HandleFunc("/login/sso/callback", loginSSOCallbackHandler(cfgDB))

	// "GET /{$}" (exact-root-only), not the bare "/" catch-all subtree
	// pattern: a bare "/" would match every path/method not otherwise
	// registered, which — combined with the method-specific "/api/..."
	// patterns below — would silently suppress net/http's automatic 405
	// Method Not Allowed handling for all of them (a request with the wrong
	// method on a registered path would fall through to this handler instead
	// of getting a 405), turning every wrong-verb request that should 405
	// into a 404/login-redirect from statusHandler instead.
	mux.HandleFunc("GET /{$}", requirePermission(cfgDB, true, permViewStatus)(statusHandler(rec, pageTemplate)))
	mux.HandleFunc("/config", requirePermission(cfgDB, true, permViewConfig)(configPageHandler))
	mux.HandleFunc("/users", requireSuperuser(cfgDB, true)(usersPageHandler))
	mux.HandleFunc("/security-log", requireSuperuser(cfgDB, true)(securityLogPageHandler))

	// Config: GET /api/config is the whole-config bundle (api/database
	// sections plus a raw-YAML view — the Maps tab is served by the /api/maps
	// family below instead). Each section has its own GET (view_config) and
	// PUT (edit_config_{api,database,sso}) registered separately, so the
	// permission each method requires is visible right here rather than
	// buried in a per-handler method switch.
	mux.HandleFunc("GET /api/config", requirePermission(cfgDB, false, permViewConfig)(configAPIHandler(cfgDB, webServer)))
	mux.HandleFunc("GET /api/config/api", requirePermission(cfgDB, false, permViewConfig)(getAPISectionHandler(cfgDB)))
	mux.HandleFunc("PUT /api/config/api",
		requirePermission(cfgDB, false, permEditConfigAPI)(saveAPISectionHandler(cfgDB, webServer, reload)))
	mux.HandleFunc("GET /api/config/database",
		requirePermission(cfgDB, false, permViewConfig)(getDatabaseSectionHandler(cfgDB)))
	mux.HandleFunc("PUT /api/config/database",
		requirePermission(cfgDB, false, permEditConfigDatabase)(saveDatabaseSectionHandler(cfgDB, webServer, reload)))
	mux.HandleFunc("GET /api/config/sso", requirePermission(cfgDB, false, permViewConfig)(getSSOConfigHandler(cfgDB)))
	mux.HandleFunc("PUT /api/config/sso",
		requirePermission(cfgDB, false, permEditConfigSSO)(saveSSOConfigHandler(cfgDB)))

	// Maps: a first-class CRUD resource (see maps.go), not a config section —
	// each map is independently addressable/mutable, so adding or editing one
	// map no longer requires resubmitting every other configured map.
	mux.HandleFunc("GET /api/maps", requirePermission(cfgDB, false, permViewConfig)(listMapsAPIHandler(cfgDB)))
	mux.HandleFunc("POST /api/maps",
		requirePermission(cfgDB, false, permEditConfigMaps)(createMapAPIHandler(cfgDB, reload, createMapOverlays)))
	mux.HandleFunc("GET /api/maps/{id}", requirePermission(cfgDB, false, permViewConfig)(getMapAPIHandler(cfgDB)))
	mux.HandleFunc("PUT /api/maps/{id}",
		requirePermission(cfgDB, false, permEditConfigMaps)(updateMapAPIHandler(cfgDB, reload, updateMapOverlays)))
	mux.HandleFunc("DELETE /api/maps/{id}",
		requirePermission(cfgDB, false, permEditConfigMaps)(
			deleteMapAPIHandler(cfgDB, reload, deleteMapObjects, deleteMapOverlays),
		))
	mux.HandleFunc("POST /api/maps/{id}/sync",
		requirePermission(cfgDB, false, permTriggerSync)(syncMapAPIHandler(syncMap)))

	mux.HandleFunc("GET /api/users", requireSuperuser(cfgDB, false)(listUsersAPIHandler(cfgDB)))
	mux.HandleFunc("POST /api/users", requireSuperuser(cfgDB, false)(createUserAPIHandler(cfgDB)))
	mux.HandleFunc("GET /api/users/{id}", requireSuperuser(cfgDB, false)(getUserAPIHandler(cfgDB)))
	mux.HandleFunc("PUT /api/users/{id}", requireSuperuser(cfgDB, false)(updateUserAPIHandler(cfgDB)))
	mux.HandleFunc("PATCH /api/users/{id}", requireSuperuser(cfgDB, false)(updateUserAPIHandler(cfgDB)))
	mux.HandleFunc("DELETE /api/users/{id}", requireSuperuser(cfgDB, false)(deleteUserAPIHandler(cfgDB)))
	mux.HandleFunc("GET /api/security-log", requireSuperuser(cfgDB, false)(securityLogAPIHandler(cfgDB)))

	return &http.Server{
		Addr:              addr,
		Handler:           setupGate(cfgDB, mux),
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

// statusHandler serves the status page (see web/status.html) — the one page
// in this package that needs server-side templating (StartedAt, Runs,
// Results, ...), unlike every other page here. tmpl is built once in New,
// since resolving its {{FOOTER}} marker needs the build version/commit only
// New receives.
func statusHandler(rec *status.Recorder, tmpl *template.Template) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")

		if err := tmpl.Execute(w, rec.Snapshot()); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	}
}
