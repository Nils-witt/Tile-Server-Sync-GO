package runner

import (
	"testing"
	"time"

	"github.com/Nils-witt/Tile-Server-Sync-GO/internal/config"
)

// validMaps fills in the versions ValidateMaps requires and runs it, since
// that's what parses each map's Interval for SyncInterval to return.
func validMaps(t *testing.T, maps []config.MapTarget) []config.MapTarget {
	t.Helper()

	for i := range maps {
		maps[i].APIID = "a"
		maps[i].Versions = []string{"current"}
	}

	cfg := &config.Config{APIs: []config.API{{ID: "a"}}, Maps: maps}
	if err := cfg.ValidateMaps(); err != nil {
		t.Fatalf("ValidateMaps: %v", err)
	}

	return cfg.Maps
}

func TestScheduleTick(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name         string
		maps         []config.MapTarget
		lastSync     map[string]time.Time
		wantDue      []string
		wantInterval map[string]time.Duration
		wantWait     time.Duration
		wantHaveWait bool
	}{
		{
			name:    "never-synced map is due once",
			maps:    []config.MapTarget{{ID: "a"}},
			wantDue: []string{"a"},
		},
		{
			name:         "never-synced map with interval is due and reports its interval",
			maps:         []config.MapTarget{{ID: "a", Interval: "1m"}},
			wantDue:      []string{"a"},
			wantInterval: map[string]time.Duration{"a": time.Minute},
		},
		{
			name:     "one-shot map already synced is never due again",
			maps:     []config.MapTarget{{ID: "a"}},
			lastSync: map[string]time.Time{"a": now.Add(-time.Hour)},
		},
		{
			name:         "interval elapsed makes map due",
			maps:         []config.MapTarget{{ID: "a", Interval: "1m"}},
			lastSync:     map[string]time.Time{"a": now.Add(-time.Minute)},
			wantDue:      []string{"a"},
			wantInterval: map[string]time.Duration{"a": time.Minute},
		},
		{
			name:         "interval not elapsed contributes remaining wait",
			maps:         []config.MapTarget{{ID: "a", Interval: "1m"}},
			lastSync:     map[string]time.Time{"a": now.Add(-20 * time.Second)},
			wantWait:     40 * time.Second,
			wantHaveWait: true,
		},
		{
			name: "shortest remaining wait wins",
			maps: []config.MapTarget{
				{ID: "a", Interval: "1m"},
				{ID: "b", Interval: "10m"},
			},
			lastSync: map[string]time.Time{
				"a": now.Add(-50 * time.Second),
				"b": now.Add(-time.Minute),
			},
			wantWait:     10 * time.Second,
			wantHaveWait: true,
		},
		{
			name:     "disabled map is skipped entirely",
			maps:     []config.MapTarget{{ID: "a", Interval: "1m", Disabled: true}},
			lastSync: map[string]time.Time{"a": now.Add(-20 * time.Second)},
		},
		{
			name: "due and waiting maps mixed",
			maps: []config.MapTarget{
				{ID: "a", Interval: "1m"},
				{ID: "b", Interval: "5m"},
				{ID: "c"},
			},
			lastSync: map[string]time.Time{
				"a": now.Add(-2 * time.Minute),
				"b": now.Add(-time.Minute),
			},
			wantDue:      []string{"a", "c"},
			wantInterval: map[string]time.Duration{"a": time.Minute},
			wantWait:     4 * time.Minute,
			wantHaveWait: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			due, dueIntervals, wait, haveWait := scheduleTick(validMaps(t, tt.maps), tt.lastSync, now)

			if len(due) != len(tt.wantDue) {
				t.Fatalf("due = %v, want %v", due, tt.wantDue)
			}

			for _, id := range tt.wantDue {
				if _, ok := due[id]; !ok {
					t.Errorf("due = %v, missing %q", due, id)
				}
			}

			if len(dueIntervals) != len(tt.wantInterval) {
				t.Fatalf("dueIntervals = %v, want %v", dueIntervals, tt.wantInterval)
			}

			for id, want := range tt.wantInterval {
				if got := dueIntervals[id]; got != want {
					t.Errorf("dueIntervals[%q] = %v, want %v", id, got, want)
				}
			}

			if haveWait != tt.wantHaveWait || wait != tt.wantWait {
				t.Errorf("wait = (%v, %v), want (%v, %v)", wait, haveWait, tt.wantWait, tt.wantHaveWait)
			}
		})
	}
}
