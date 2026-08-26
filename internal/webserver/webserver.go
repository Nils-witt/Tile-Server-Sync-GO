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
// per-map "Sync" button hitting "/api/sync/{mapID}") and a config editor at
// "/config" (backed by a JSON API at "/api/config") that reads and writes
// cfgDB. It does not start listening; call ListenAndServe (typically in a
// goroutine).
//
// A successful POST /api/config save also calls reload itself, so the
// running process picks up the change immediately without a separate
// action — see saveConfig. None of the config editor or sync endpoints have
// authentication of their own, matching the status page they sit alongside —
// only expose addr on a trusted network. webServer is the fixed,
// bootstrap-file-sourced WebServer value: the config editor always displays
// it for context but can never change it, since applying a changed
// webServer.enabled/address needs a process restart the server itself can't
// safely trigger mid-request.
func New(
	addr string, rec *status.Recorder, cfgDB *configdb.Store, webServer config.WebServer,
	reload func(context.Context) error, syncMap func(context.Context, string) (int, error),
) *http.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/", statusHandler(rec))
	mux.HandleFunc("/config", configPageHandler)
	mux.HandleFunc("/api/config", configAPIHandler(cfgDB, webServer, reload))
	mux.HandleFunc("/api/sync/", syncMapAPIHandler(syncMap))

	return &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
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
<title>go-sync-objects status</title>
<meta http-equiv="refresh" content="10">
<style>` + baseCSS + `</style>
</head>
<body>
<div class="topbar">
  <span class="brand">go-sync-objects</span>
  <nav>
    <a href="/" class="active">Status</a>
    <a href="/config">Config</a>
  </nav>
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

<script>
(function () {
  "use strict";

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
