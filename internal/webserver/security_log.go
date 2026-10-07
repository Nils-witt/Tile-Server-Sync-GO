package webserver

import (
	"net/http"
	"strconv"
	"time"

	"github.com/Nils-witt/Tile-Server-Sync-GO/internal/runner"
)

const (
	defaultSecurityLogLimit = 200
	maxSecurityLogLimit     = 1000
)

// securityLogEntryDTO is a runner.SecurityLogEntry as sent to the JSON API.
type securityLogEntryDTO struct {
	At         string `json:"at"`
	EventType  string `json:"eventType"`
	Username   string `json:"username"`
	RemoteAddr string `json:"remoteAddr"`
	Detail     string `json:"detail"`
}

func toSecurityLogEntryDTO(e runner.SecurityLogEntry) securityLogEntryDTO {
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
func securityLogAPIHandler(run Runner) http.HandlerFunc {
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

		entries, err := run.SecurityLog(r.Context(), limit)
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
