package webserver

import (
	"Tile-Server-Sync-GO/internal/config"
	"fmt"
	"maps"
	"slices"
	"strings"
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

// diffDatabase compares two config.Database values field by field. DSN
// typically embeds credentials, so like API.Password it's only reported as
// changed, never in plaintext.
func diffDatabase(before, after config.Database) []string {
	var changes []string

	if before.DSN != after.DSN {
		changes = append(changes, "dsn changed")
	}

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

	return changes
}

// diffMapFields compares two config.MapTarget values field by field
// (name/versions/interval/disabled/staticColumns) — used by maps.go's
// updateMapAPIHandler to log what a PUT /api/maps/{id} actually changed.
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

// permissionFields lists a config.SSOPermissions' boolean fields alongside
// the label used to describe each in a security_log detail string (see
// grantedPermissions).
var permissionFields = []struct {
	label string
	get   func(config.SSOPermissions) bool
}{
	{"viewStatus", func(p config.SSOPermissions) bool { return p.ViewStatus }},
	{"triggerSync", func(p config.SSOPermissions) bool { return p.TriggerSync }},
	{"viewConfig", func(p config.SSOPermissions) bool { return p.ViewConfig }},
	{"editConfigApi", func(p config.SSOPermissions) bool { return p.EditConfigAPI }},
	{"editConfigDatabase", func(p config.SSOPermissions) bool { return p.EditConfigDatabase }},
	{"editConfigMaps", func(p config.SSOPermissions) bool { return p.EditConfigMaps }},
}

// grantedPermissions lists the permissions set to true in perms, for
// recording what a login's groups granted (see meAPIHandler).
func grantedPermissions(perms config.SSOPermissions) []string {
	var granted []string

	for _, f := range permissionFields {
		if f.get(perms) {
			granted = append(granted, f.label)
		}
	}

	return granted
}
