package runner

import (
	"context"
	"log"

	"github.com/Nils-witt/Tile-Server-Sync-GO/internal/configdb"
)

// SecurityLogEntry is one row of the audit trail.
type SecurityLogEntry = configdb.SecurityLogEntry

// LogSecurityEvent appends one row to the security log. Best-effort: a
// failure to write it is only logged, never returned — the audit trail must
// not block or fail the action that triggered it. The runner records its
// own change events (config_saved, map_*); this is exported for events only
// the caller knows about, such as SSO logins.
func (r *Runner) LogSecurityEvent(ctx context.Context, actor Actor, eventType, detail string) {
	if err := r.cfgDB.LogSecurityEvent(ctx, eventType, actor.Username, actor.RemoteAddr, detail); err != nil {
		log.Printf("security log: %v", err)
	}
}

// SecurityLog returns the most recent security log entries, newest first,
// capped at limit.
func (r *Runner) SecurityLog(ctx context.Context, limit int) ([]SecurityLogEntry, error) {
	return r.cfgDB.ListSecurityLog(ctx, limit)
}
