package webserver

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/Nils-witt/Tile-Server-Sync-GO/internal/config"
)

func TestGroupsFromClaim(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		raw  string
		want []string
	}{
		{``, nil},
		{`["a","b"]`, []string{"a", "b"}},
		{`"solo"`, []string{"solo"}},
		{`""`, nil},
		{`42`, nil},
		{`{"a":1}`, nil},
	} {
		if got := groupsFromClaim(json.RawMessage(tc.raw)); !slices.Equal(got, tc.want) {
			t.Errorf("groupsFromClaim(%s) = %v, want %v", tc.raw, got, tc.want)
		}
	}
}

func TestGroupGrantsUnion(t *testing.T) {
	t.Parallel()

	mapping := map[string]config.SSOGroupGrant{
		"ops":    {SSOPermissions: config.SSOPermissions{ViewStatus: true, TriggerSync: true}},
		"config": {SSOPermissions: config.SSOPermissions{ViewConfig: true, EditConfigMaps: true}},
		"admins": {Superuser: true},
	}

	perms, superuser := groupGrants(mapping, []string{"ops", "config", "unmapped"})
	got := unionPermissions(config.SSOPermissions{EditConfigAPI: true}, perms)

	want := config.SSOPermissions{
		ViewStatus: true, TriggerSync: true, ViewConfig: true, EditConfigAPI: true, EditConfigMaps: true,
	}
	if got != want || superuser {
		t.Errorf("got %+v superuser=%v, want %+v superuser=false", got, superuser, want)
	}

	if _, superuser := groupGrants(mapping, []string{"ops", "admins"}); !superuser {
		t.Error("admins group did not grant superuser")
	}

	if none, superuser := groupGrants(mapping, []string{"unmapped"}); none != (config.SSOPermissions{}) || superuser {
		t.Errorf("unmapped group granted %+v superuser=%v", none, superuser)
	}
}
