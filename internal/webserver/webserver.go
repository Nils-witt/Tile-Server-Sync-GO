// Package webserver exposes a minimal HTTP status page showing sync status
// and recent log output, backed by an internal/status.Recorder.
package webserver

import (
	"context"
	"go-sync-objects/internal/config"
	"go-sync-objects/internal/configdb"
	"go-sync-objects/internal/status"
	"html/template"
	"net/http"
	"time"
)

// New builds an *http.Server serving the status page at "/" (with a
// per-map "Sync" button hitting "/api/sync/{mapID}"), a config editor at
// "/config" (backed by a JSON API at "/api/config" and its per-section save
// endpoints), and a user management page at "/users" — all gated behind a
// session-cookie login (see auth.go) and the logged-in user's permissions
// (see configdb.Permissions). While no account exists yet, every request is
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
	reload func(context.Context) error, syncMap func(context.Context, string) (int, error),
) *http.Server {
	mux := http.NewServeMux()

	mux.HandleFunc("/setup", setupHandler(cfgDB))
	mux.HandleFunc("/login", loginHandler(cfgDB))
	mux.HandleFunc("/logout", logoutHandler(cfgDB))
	mux.HandleFunc("/api/me", requireUser(cfgDB, false)(meAPIHandler))

	mux.HandleFunc("/", requirePermission(cfgDB, true, permViewStatus)(statusHandler(rec)))
	mux.HandleFunc("/config", requirePermission(cfgDB, true, permViewConfig)(configPageHandler))
	mux.HandleFunc("/users", requireSuperuser(cfgDB, true)(usersPageHandler))

	mux.HandleFunc("/api/config", requirePermission(cfgDB, false, permViewConfig)(configAPIHandler(cfgDB, webServer)))
	mux.HandleFunc("/api/config/api",
		requirePermission(cfgDB, false, permEditConfigAPI)(saveAPISectionHandler(cfgDB, webServer, reload)))
	mux.HandleFunc("/api/config/database",
		requirePermission(cfgDB, false, permEditConfigDatabase)(saveDatabaseSectionHandler(cfgDB, webServer, reload)))
	mux.HandleFunc("/api/config/maps",
		requirePermission(cfgDB, false, permEditConfigMaps)(saveMapsSectionHandler(cfgDB, webServer, reload)))
	mux.HandleFunc("/api/config/raw",
		requirePermission(cfgDB, false, permAllEditConfig)(saveRawConfigHandler(cfgDB, webServer, reload)))
	mux.HandleFunc("/api/sync/", requirePermission(cfgDB, false, permTriggerSync)(syncMapAPIHandler(syncMap)))

	mux.HandleFunc("/api/users", requireSuperuser(cfgDB, false)(usersAPIHandler(cfgDB)))
	mux.HandleFunc("/api/users/", requireSuperuser(cfgDB, false)(userAPIHandler(cfgDB)))

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

// permAllEditConfig gates the whole-config raw YAML save mode, which can
// touch any section, behind holding all three edit_config_* permissions
// together.
func permAllEditConfig(p configdb.Permissions) bool {
	return p.EditConfigAPI && p.EditConfigDatabase && p.EditConfigMaps
}

func statusHandler(rec *status.Recorder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")

		if err := pageTemplate.Execute(w, rec.Snapshot()); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	}
}

const statusPageHTML = `<!DOCTYPE html>
<html>
<head>
<meta charset="utf-8">
<script>` + themeInitScript + `</script>
<title>go-sync-objects status</title>
<meta http-equiv="refresh" content="10">
<style>` + baseCSS + `</style>
</head>
<body>
<div class="topbar">
  <span class="brand">go-sync-objects</span>
  <nav>
    <a href="/" class="active">Status</a>
    <a href="/config" id="nav-config">Config</a>
    <a href="/users" id="nav-users">Users</a>
  </nav>
  <nav id="account-nav"></nav>
  <button type="button" id="theme-toggle" class="theme-toggle" aria-label="Toggle dark mode"></button>
</div>

<main>

<div id="msg" class="banner"></div>

<div class="stat-grid">
  <div class="stat-card">
    <div class="label">Started</div>
    <div class="value" style="font-size:1.1rem">{{.StartedAt.Format "2006-01-02 15:04:05"}}</div>
  </div>
  <div class="stat-card">
    <div class="label">Runs</div>
    <div class="value">{{.Runs}}</div>
  </div>
  <div class="stat-card">
    <div class="label">Last run</div>
    {{if .LastRunAt.IsZero}}
      <div class="value" style="font-size:1.1rem">never</div>
    {{else}}
      <div class="value" style="font-size:1.1rem">{{.LastRunAt.Format "2006-01-02 15:04:05"}}</div>
      <div class="sub">{{if .LastRunErr}}<span class="badge err">error</span>{{else}}<span class="badge ok">ok</span>{{end}}</div>
    {{end}}
  </div>
  <div class="stat-card">
    <div class="label">Total objects synced</div>
    <div class="value">{{.TotalSynced}}</div>
  </div>
</div>

{{if .LastRunErr}}
<div class="card" style="border-color:var(--danger); background:var(--danger-bg);">
  <strong style="color:var(--danger)">Last run error:</strong> {{.LastRunErr}}
</div>
{{end}}

<div class="card">
  <h2>Last result per map/version</h2>
  {{if not .Results}}
  <p class="hint">No syncs yet.</p>
  {{else}}
  <table>
  <tr><th>Map</th><th>Version</th><th>Synced</th><th>At</th><th>Status</th><th></th></tr>
  {{range .Results}}
  <tr>
    <td>{{.MapID}}</td>
    <td>{{.Version}}</td>
    <td>{{.Synced}}</td>
    <td>{{.At.Format "2006-01-02 15:04:05"}}</td>
    <td>{{if .Err}}<span class="badge err" title="{{.Err}}">error</span>{{else}}<span class="badge ok">ok</span>{{end}}</td>
    <td><button type="button" class="sync-map-btn" data-map-id="{{.MapID}}">Sync</button></td>
  </tr>
  {{end}}
  </table>
  {{end}}
</div>

<div class="card">
  <details class="log-card" open>
    <summary>Recent log output</summary>
    <pre class="log">{{range .Logs}}{{.}}{{end}}</pre>
  </details>
</div>

</main>

<script>` + accountNavJS + themeToggleJS + `</script>
<script>
(function () {
  "use strict";

  initThemeToggle();

  initAccountNav(function (me) {
    if (!me.permissions.triggerSync) {
      document.querySelectorAll(".sync-map-btn").forEach(function (btn) { btn.style.display = "none"; });
    }
  });

  var msg = document.getElementById("msg");
  function showMessage(ok, text) {
    msg.className = "banner " + (ok ? "ok" : "err");
    msg.textContent = text;
  }

  document.querySelectorAll(".sync-map-btn").forEach(function (btn) {
    btn.addEventListener("click", function () {
      var id = btn.dataset.mapId;
      var original = btn.textContent;
      btn.disabled = true;
      btn.textContent = "Syncing…";
      showMessage(true, "Syncing " + id + "…");

      fetch("/api/sync/" + encodeURIComponent(id), { method: "POST" })
        .then(function (r) { return r.json().then(function (b) { return { ok: r.ok, body: b }; }); })
        .then(function (res) {
          if (res.ok) {
            location.reload();
          } else {
            showMessage(false, "Sync failed for " + id + ": " + (res.body.error || "unknown error"));
            btn.disabled = false;
            btn.textContent = original;
          }
        })
        .catch(function (e) {
          showMessage(false, "Sync failed for " + id + ": " + e);
          btn.disabled = false;
          btn.textContent = original;
        });
    });
  });
})();
</script>
</body>
</html>
`

var pageTemplate = template.Must(template.New("status").Parse(statusPageHTML))
