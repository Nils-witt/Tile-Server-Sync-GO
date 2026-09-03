package webserver

import (
	"Tile-Server-Sync-GO/internal/status"
	"net/http"
	"time"
)

// mapVersionResultDTO is a status.MapVersionResult as sent to the JSON API.
type mapVersionResultDTO struct {
	MapID   string `json:"mapId"`
	Version string `json:"version"`
	Synced  int    `json:"synced"`
	Err     string `json:"err,omitempty"`
	At      string `json:"at"`
}

// statusResponse is what GET /api/status returns: a status.Snapshot with its
// timestamps formatted as RFC3339 strings, the same JSON-friendly shape
// securityLogEntryDTO uses for configdb.SecurityLogEntry. Replaces the old
// server-rendered status.html template (see web/status.html, removed) — the
// SPA's status page polls this every 10s to match that page's old
// <meta http-equiv="refresh" content="10">.
type statusResponse struct {
	StartedAt   string                `json:"startedAt"`
	Runs        int                   `json:"runs"`
	LastRunAt   string                `json:"lastRunAt,omitempty"`
	LastRunErr  string                `json:"lastRunErr,omitempty"`
	TotalSynced int                   `json:"totalSynced"`
	Results     []mapVersionResultDTO `json:"results"`
	Logs        []string              `json:"logs"`
}

func toStatusResponse(s status.Snapshot) statusResponse {
	results := make([]mapVersionResultDTO, len(s.Results))
	for i, res := range s.Results {
		results[i] = mapVersionResultDTO{
			MapID: res.MapID, Version: res.Version, Synced: res.Synced, Err: res.Err,
			At: res.At.Format(time.RFC3339),
		}
	}

	resp := statusResponse{
		StartedAt: s.StartedAt.Format(time.RFC3339), Runs: s.Runs, LastRunErr: s.LastRunErr,
		TotalSynced: s.TotalSynced, Results: results, Logs: s.Logs,
	}

	if !s.LastRunAt.IsZero() {
		resp.LastRunAt = s.LastRunAt.Format(time.RFC3339)
	}

	return resp
}

// statusAPIHandler serves GET /api/status. Requires view_status.
func statusAPIHandler(rec *status.Recorder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)

			return
		}

		writeJSON(w, http.StatusOK, toStatusResponse(rec.Snapshot()))
	}
}

// versionResponse is what GET /api/version returns: the build info the old
// server-rendered footer (web/footer.html) used to splice in, now rendered
// by the SPA's own footer component instead.
type versionResponse struct {
	Version string `json:"version"`
	Commit  string `json:"commit"`
}

// versionAPIHandler serves GET /api/version, deliberately unauthenticated
// (like /api/sso/status and /api/setup-status) since the footer it feeds is
// shown on /login and /setup too, before any session exists.
func versionAPIHandler(version, commit string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)

			return
		}

		writeJSON(w, http.StatusOK, versionResponse{Version: version, Commit: commit})
	}
}
