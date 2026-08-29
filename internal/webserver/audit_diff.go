package webserver

import (
	"Tile-Server-Sync-GO/internal/config"
	"Tile-Server-Sync-GO/internal/configdb"
	"fmt"
	"maps"
	"slices"
	"strings"
)

// changesDetail joins a list of per-field change descriptions (as built by
// the diff* helpers below) into the free-form detail string appended to a
// security_log row, so every config/account change records not just that it
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
// (name/versions/interval/staticColumns) — used by maps.go's
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

	if !maps.Equal(old.StaticColumns, updated.StaticColumns) {
		changes = append(changes, "staticColumns changed")
	}

	return changes
}

// diffSSO compares two configdb.SSOConfig values field by field.
// ClientSecret is only reported as changed, matching API.Password/
// Database.DSN.
func diffSSO(before, after *configdb.SSOConfig) []string {
	var changes []string

	if before.Enabled != after.Enabled {
		changes = append(changes, fmt.Sprintf("enabled %v->%v", before.Enabled, after.Enabled))
	}

	if before.IssuerURL != after.IssuerURL {
		changes = append(changes, fmt.Sprintf("issuerUrl %q->%q", before.IssuerURL, after.IssuerURL))
	}

	if before.ClientID != after.ClientID {
		changes = append(changes, fmt.Sprintf("clientId %q->%q", before.ClientID, after.ClientID))
	}

	if before.ClientSecret != after.ClientSecret {
		changes = append(changes, "clientSecret changed")
	}

	if before.Scopes != after.Scopes {
		changes = append(changes, fmt.Sprintf("scopes %q->%q", before.Scopes, after.Scopes))
	}

	if before.ButtonLabel != after.ButtonLabel {
		changes = append(changes, fmt.Sprintf("buttonLabel %q->%q", before.ButtonLabel, after.ButtonLabel))
	}

	if before.RedirectBaseURL != after.RedirectBaseURL {
		changes = append(changes, fmt.Sprintf("redirectBaseUrl %q->%q", before.RedirectBaseURL, after.RedirectBaseURL))
	}

	if permChanges := diffPermissions(before.DefaultPermissions, after.DefaultPermissions); len(permChanges) > 0 {
		changes = append(changes, "defaultPermissions: "+strings.Join(permChanges, ", "))
	}

	return changes
}

// permissionFields lists a configdb.Permissions' boolean fields alongside
// the label used to describe each in a security_log detail string, shared by
// diffPermissions and grantedPermissions.
var permissionFields = []struct {
	label string
	get   func(configdb.Permissions) bool
}{
	{"viewStatus", func(p configdb.Permissions) bool { return p.ViewStatus }},
	{"triggerSync", func(p configdb.Permissions) bool { return p.TriggerSync }},
	{"viewConfig", func(p configdb.Permissions) bool { return p.ViewConfig }},
	{"editConfigApi", func(p configdb.Permissions) bool { return p.EditConfigAPI }},
	{"editConfigDatabase", func(p configdb.Permissions) bool { return p.EditConfigDatabase }},
	{"editConfigMaps", func(p configdb.Permissions) bool { return p.EditConfigMaps }},
	{"editConfigSso", func(p configdb.Permissions) bool { return p.EditConfigSSO }},
}

// diffPermissions reports which individual permission bits flipped between
// before and after, e.g. "editConfigApi: false->true".
func diffPermissions(before, after configdb.Permissions) []string {
	var changes []string

	for _, f := range permissionFields {
		b, a := f.get(before), f.get(after)
		if b != a {
			changes = append(changes, fmt.Sprintf("%s %v->%v", f.label, b, a))
		}
	}

	return changes
}

// grantedPermissions lists the permissions set to true in perms, for
// recording the initial grant on account creation (there's no "before" to
// diff against).
func grantedPermissions(perms configdb.Permissions) []string {
	var granted []string

	for _, f := range permissionFields {
		if f.get(perms) {
			granted = append(granted, f.label)
		}
	}

	return granted
}
