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

// New builds an *http.Server serving the status page at "/" and a config
// editor at "/config" (backed by a JSON API at "/api/config") that reads and
// writes cfgDB, plus two action endpoints: "/api/reload" calls reload to
// make the running process pick up cfgDB's current contents immediately,
// without a restart, and "/api/sync" calls syncNow to run a sync immediately
// instead of waiting for the next scheduled interval tick. It does not start
// listening; call ListenAndServe (typically in a goroutine).
//
// None of the config editor, reload, or sync endpoints have authentication
// of their own, matching the status page they sit alongside — only expose
// addr on a trusted network. webServer is the fixed, bootstrap-file-sourced
// WebServer value: the config editor always displays it for context but can
// never change it, since applying a changed webServer.enabled/address needs
// a process restart the server itself can't safely trigger mid-request.
func New(
	addr string, rec *status.Recorder, cfgDB *configdb.Store, webServer config.WebServer,
	reload func(context.Context) error, syncNow func(context.Context) (int, error),
) *http.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/", statusHandler(rec))
	mux.HandleFunc("/config", configPageHandler)
	mux.HandleFunc("/api/config", configAPIHandler(cfgDB, webServer))
	mux.HandleFunc("/api/reload", reloadAPIHandler(reload))
	mux.HandleFunc("/api/sync", syncAPIHandler(syncNow))

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

var pageTemplate = template.Must(template.New("status").Parse(`<!DOCTYPE html>
<html>
<head>
<meta charset="utf-8">
<title>go-sync-objects status</title>
<meta http-equiv="refresh" content="10">
<style>
  body { font-family: system-ui, sans-serif; margin: 2rem; color: #1a1a1a; }
  h1 { font-size: 1.3rem; }
  table { border-collapse: collapse; margin: 1rem 0; }
  th, td { border: 1px solid #ccc; padding: 0.3rem 0.6rem; text-align: left; font-size: 0.9rem; }
  th { background: #f0f0f0; }
  .ok { color: #1a7f37; }
  .err { color: #c0392b; }
  pre { background: #111; color: #ddd; padding: 1rem; overflow-x: auto; max-height: 50vh; font-size: 0.85rem; }
</style>
</head>
<body>
<h1>go-sync-objects</h1>

<p><a href="/config">Edit config &rarr;</a></p>

<p>
  Started: {{.StartedAt.Format "2006-01-02 15:04:05"}}<br>
  Runs: {{.Runs}}<br>
  {{if .LastRunAt.IsZero}}
    Last run: never
  {{else}}
    Last run: {{.LastRunAt.Format "2006-01-02 15:04:05"}}
    {{if .LastRunErr}}<span class="err">(error: {{.LastRunErr}})</span>{{else}}<span class="ok">(ok)</span>{{end}}
  {{end}}<br>
  Total objects synced: {{.TotalSynced}}
</p>

<h2>Last result per map/version</h2>
{{if not .Results}}
<p>No syncs yet.</p>
{{else}}
<table>
<tr><th>Map</th><th>Version</th><th>Synced</th><th>At</th><th>Status</th></tr>
{{range .Results}}
<tr>
  <td>{{.MapID}}</td>
  <td>{{.Version}}</td>
  <td>{{.Synced}}</td>
  <td>{{.At.Format "2006-01-02 15:04:05"}}</td>
  <td>{{if .Err}}<span class="err">{{.Err}}</span>{{else}}<span class="ok">ok</span>{{end}}</td>
</tr>
{{end}}
</table>
{{end}}

<h2>Recent log output</h2>
<pre>{{range .Logs}}{{.}}{{end}}</pre>

</body>
</html>
`))
