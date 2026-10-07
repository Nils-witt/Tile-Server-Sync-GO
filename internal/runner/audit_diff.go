package runner

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/Nils-witt/Tile-Server-Sync-GO/internal/config"
)

// changesDetail joins a list of per-field change descriptions (as built by
// the diff* helpers below) into the free-form detail string appended to a
// security_log row, so every config/map change records not just that it
// happened but what changed. An empty change set (a save that touched no
// field, e.g. re-submitting the same form) is reported explicitly rather
// than left silent.
func changesDetail(changes []string) string {
	if len(changes) == 0 {
		return "no changes"
	}

	return "changed: " + strings.Join(changes, ", ")
}

// diffAPI compares two config.API values field by field. Password/Token are
// never logged in plaintext — only whether they changed — since the security
// log is readable by every superuser, not just the ones permitted to edit
// this section.
func diffAPI(before, after config.API) []string {
	var changes []string

	if before.BaseURL != after.BaseURL {
		changes = append(changes, fmt.Sprintf("baseUrl %q->%q", before.BaseURL, after.BaseURL))
	}

	if before.Username != after.Username {
		changes = append(changes, fmt.Sprintf("username %q->%q", before.Username, after.Username))
	}

	if before.Password != after.Password {
		changes = append(changes, "password changed")
	}

	if before.Token != after.Token {
		changes = append(changes, "token changed")
	}

	return changes
}

// diffDatabase compares two config.Database values field by field. Like
// API.Password, Password is only reported as changed, never in plaintext.
func diffDatabase(before, after config.Database) []string {
	changes := diffDatabaseConnection(before, after)

	if before.Table != after.Table {
		changes = append(changes, fmt.Sprintf("table %q->%q", before.Table, after.Table))
	}

	if before.PruneMissing != after.PruneMissing {
		changes = append(changes, fmt.Sprintf("pruneMissing %v->%v", before.PruneMissing, after.PruneMissing))
	}

	if before.SyncOverlays != after.SyncOverlays {
		changes = append(changes, fmt.Sprintf("syncOverlays %v->%v", before.SyncOverlays, after.SyncOverlays))
	}

	if !maps.Equal(before.Columns, after.Columns) {
		changes = append(changes, "columns changed")
	}

	// The PEM text itself is too long to be useful in the log.
	if before.TLSCACert != after.TLSCACert {
		changes = append(changes, "tlsCaCert changed")
	}

	return changes
}

// diffMapFields compares two config.MapTarget values field by field
// (name/versions/interval/disabled/staticColumns) — used by UpdateMap to
// log what an update actually changed.
func diffMapFields(old, updated config.MapTarget) []string {
	var changes []string

	if old.Name != updated.Name {
		changes = append(changes, fmt.Sprintf("name %q->%q", old.Name, updated.Name))
	}

	if !slices.Equal(old.Versions, updated.Versions) {
		changes = append(changes, fmt.Sprintf("versions %v->%v", old.Versions, updated.Versions))
	}

	if old.Interval != updated.Interval {
		changes = append(changes, fmt.Sprintf("interval %q->%q", old.Interval, updated.Interval))
	}

	if old.Disabled != updated.Disabled {
		changes = append(changes, fmt.Sprintf("disabled %v->%v", old.Disabled, updated.Disabled))
	}

	if !maps.Equal(old.StaticColumns, updated.StaticColumns) {
		changes = append(changes, "staticColumns changed")
	}

	return changes
}

// diffDatabaseConnection compares the connection components of two
// config.Database values (split out of diffDatabase to keep its cyclomatic
// complexity down).
func diffDatabaseConnection(before, after config.Database) []string {
	var changes []string

	for _, f := range []struct{ name, before, after string }{
		{"host", before.Host, after.Host},
		{"user", before.User, after.User},
		{"name", before.Name, after.Name},
		{"params", before.Params, after.Params},
	} {
		if f.before != f.after {
			changes = append(changes, fmt.Sprintf("%s %q->%q", f.name, f.before, f.after))
		}
	}

	if before.Port != after.Port {
		changes = append(changes, fmt.Sprintf("port %d->%d", before.Port, after.Port))
	}

	if before.Password != after.Password {
		changes = append(changes, "password changed")
	}

	if before.TLS != after.TLS {
		changes = append(changes, fmt.Sprintf("tls %v->%v", before.TLS, after.TLS))
	}

	if before.TLSSkipVerify != after.TLSSkipVerify {
		changes = append(changes, fmt.Sprintf("tlsSkipVerify %v->%v", before.TLSSkipVerify, after.TLSSkipVerify))
	}

	return changes
}
