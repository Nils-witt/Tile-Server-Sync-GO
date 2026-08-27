package configdb

import (
	"context"
	"fmt"
	"time"
)

// securityLogSchemaStatements creates the security_log table, an
// append-only audit trail of logins and changes made to config/users
// through the web server (see internal/webserver's calls to
// LogSecurityEvent). Run through the same ensureSchema loop as the other
// schema slices in this package.
var securityLogSchemaStatements = []string{
	`CREATE TABLE IF NOT EXISTS security_log (
		id          INTEGER PRIMARY KEY AUTOINCREMENT,
		at          TEXT NOT NULL,
		event_type  TEXT NOT NULL,
		username    TEXT NOT NULL DEFAULT '',
		remote_addr TEXT NOT NULL DEFAULT '',
		detail      TEXT NOT NULL DEFAULT ''
	)`,
	`CREATE INDEX IF NOT EXISTS idx_security_log_id ON security_log(id DESC)`,
}

// SecurityLogEntry is one row of the audit trail.
type SecurityLogEntry struct {
	ID         int64
	At         time.Time
	EventType  string
	Username   string
	RemoteAddr string
	Detail     string
}

// LogSecurityEvent appends one entry to the security log. Callers treat a
// failure here as best-effort (log and continue): the audit trail must never
// block or fail the login/save/user-edit action that triggered it.
func (s *Store) LogSecurityEvent(ctx context.Context, eventType, username, remoteAddr, detail string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO security_log (at, event_type, username, remote_addr, detail) VALUES (?, ?, ?, ?, ?)`,
		time.Now().UTC().Format(timeFormat), eventType, username, remoteAddr, detail)
	if err != nil {
		return fmt.Errorf("log security event: %w", err)
	}

	return nil
}

// ListSecurityLog returns the most recent entries, newest first, capped at
// limit rows.
func (s *Store) ListSecurityLog(ctx context.Context, limit int) ([]SecurityLogEntry, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, at, event_type, username, remote_addr, detail
		 FROM security_log ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("list security log: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var entries []SecurityLogEntry

	for rows.Next() {
		var (
			e  SecurityLogEntry
			at string
		)

		if err := rows.Scan(&e.ID, &at, &e.EventType, &e.Username, &e.RemoteAddr, &e.Detail); err != nil {
			return nil, fmt.Errorf("scan security log entry: %w", err)
		}

		if parsed, err := time.Parse(timeFormat, at); err == nil {
			e.At = parsed
		}

		entries = append(entries, e)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list security log: %w", err)
	}

	return entries, nil
}
