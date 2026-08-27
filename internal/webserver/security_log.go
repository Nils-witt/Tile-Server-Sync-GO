package webserver

import (
	"go-sync-objects/internal/configdb"
	"log"
	"net/http"
	"strconv"
	"time"
)

// logSecurityEvent appends one row to the security log (see
// configdb.Store.LogSecurityEvent). Best-effort: a failure to write the log
// entry is only logged to stderr, never returned to the caller — the audit
// trail must not block or fail the login/save/user-edit action that
// triggered it.
func logSecurityEvent(r *http.Request, cfgDB *configdb.Store, eventType, username, detail string) {
	if err := cfgDB.LogSecurityEvent(r.Context(), eventType, username, r.RemoteAddr, detail); err != nil {
		log.Printf("security log: %v", err)
	}
}

const (
	defaultSecurityLogLimit = 200
	maxSecurityLogLimit     = 1000
)

// securityLogEntryDTO is a configdb.SecurityLogEntry as sent to the JSON API.
type securityLogEntryDTO struct {
	At         string `json:"at"`
	EventType  string `json:"eventType"`
	Username   string `json:"username"`
	RemoteAddr string `json:"remoteAddr"`
	Detail     string `json:"detail"`
}

func toSecurityLogEntryDTO(e configdb.SecurityLogEntry) securityLogEntryDTO {
	return securityLogEntryDTO{
		At: e.At.Format(time.RFC3339), EventType: e.EventType,
		Username: e.Username, RemoteAddr: e.RemoteAddr, Detail: e.Detail,
	}
}

// securityLogAPIHandler serves GET /api/security-log, reachable only by
// superusers (see requireSuperuser in webserver.go) — the log can contain
// remote addresses and other account-management detail not meant for every
// logged-in user. An optional "limit" query parameter caps how many recent
// entries are returned (default defaultSecurityLogLimit, hard-capped at
// maxSecurityLogLimit).
func securityLogAPIHandler(cfgDB *configdb.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)

			return
		}

		limit := defaultSecurityLogLimit

		if v := r.URL.Query().Get("limit"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= maxSecurityLogLimit {
				limit = n
			}
		}

		entries, err := cfgDB.ListSecurityLog(r.Context(), limit)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, errorJSON(err.Error()))
			return
		}

		dtos := make([]securityLogEntryDTO, len(entries))
		for i, e := range entries {
			dtos[i] = toSecurityLogEntryDTO(e)
		}

		writeJSON(w, http.StatusOK, dtos)
	}
}

func securityLogPageHandler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/security-log" {
		http.NotFound(w, r)
		return
	}

	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)

		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(securityLogPageHTML))
}

// securityLogPageHTML is served at GET /security-log, reachable only by
// superusers. Like usersPageHTML, it's a static page driven entirely by a
// fetch call to /api/security-log, with no server-side templating.
const securityLogPageHTML = `<!DOCTYPE html>
<html>
<head>
<meta charset="utf-8">
<script>` + themeInitScript + `</script>
<title>go-sync-objects security log</title>
<style>` + baseCSS + `</style>
</head>
<body>
<div class="topbar">
  <span class="brand">go-sync-objects</span>
  <nav>
    <a href="/">Status</a>
    <a href="/config" id="nav-config">Config</a>
    <a href="/users" id="nav-users">Users</a>
    <a href="/security-log" class="active" id="nav-security-log">Security log</a>
  </nav>
  <nav id="account-nav"></nav>
  <button type="button" id="theme-toggle" class="theme-toggle" aria-label="Toggle dark mode"></button>
</div>

<main>

<div id="msg" class="banner"></div>

<div class="card">
  <h2>Security log</h2>
  <p class="hint">Logins (local and SSO, successful and failed), logouts, user-account changes, and
  config saves, newest first. This is an audit trail, not a live view &mdash; use the Refresh button
  to see new entries.</p>
  <div class="actions-row">
    <button type="button" id="refresh">Refresh</button>
  </div>
  <table id="log-table">
    <tr><th>Time</th><th>Event</th><th>Username</th><th>Remote address</th><th>Detail</th></tr>
  </table>
</div>

</main>

<script>` + accountNavJS + themeToggleJS + `</script>
<script>
(function () {
  "use strict";

  initThemeToggle();

  var msg = document.getElementById("msg");
  function showMessage(ok, text) {
    msg.className = "banner " + (ok ? "ok" : "err");
    msg.textContent = text;
  }

  function buildRow(e) {
    var tr = document.createElement("tr");
    [e.at, e.eventType, e.username, e.remoteAddr, e.detail].forEach(function (v) {
      var td = document.createElement("td");
      td.textContent = v || "";
      tr.appendChild(td);
    });
    return tr;
  }

  function load() {
    fetch("/api/security-log").then(function (r) { return r.json(); }).then(function (entries) {
      var table = document.getElementById("log-table");
      table.querySelectorAll("tr:not(:first-child)").forEach(function (tr) { tr.remove(); });
      (entries || []).forEach(function (e) { table.appendChild(buildRow(e)); });
    }).catch(function (e) { showMessage(false, "Failed to load security log: " + e); });
  }

  document.getElementById("refresh").addEventListener("click", load);

  initAccountNav();

  load();
})();
</script>
</body>
</html>
`
