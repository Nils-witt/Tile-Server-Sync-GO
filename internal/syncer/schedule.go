package syncer

import (
	"context"
	"log"
	"time"

	"github.com/Nils-witt/Tile-Server-Sync-GO/internal/config"
)

// pollInterval is how often RunLoop checks back in while the engine isn't
// configured yet (fresh install, empty SQLite-backed config), or is
// configured but none of its maps have a positive Interval yet due, waiting
// for a live edit via the web UI to make an automatic sync possible.
const pollInterval = 5 * time.Second

// RunLoop syncs each configured map on its own schedule — determined by that
// map's own Interval (see config.MapTarget) — until ctx is cancelled (e.g. by
// SIGINT/SIGTERM). Every map is synced once immediately the first time it's
// seen (covering both startup and a map added later via a live config
// reload); a map with a positive Interval then keeps re-syncing every
// Interval after that, while a map with no Interval is not automatically
// repeated. Errors from an individual sync are logged rather than aborting
// the loop, so a transient failure (e.g. a network blip) doesn't take down an
// otherwise long-running process.
//
// Each tick re-fetches e.Current() (rather than this loop capturing it
// once), so a config reload triggered via the web UI — new/removed maps,
// changed intervals, API credentials, database settings — takes effect
// immediately: e.reload pings e.wake on success, which this loop also
// selects on, so it doesn't wait out whatever wait duration was already in
// flight (which could otherwise be as long as another map's interval).
// e.runSyncMaps also serializes against a manual "sync now" request from the
// web UI, so the two can't run concurrently. lastSync (map ID -> last sync
// start time) is purely in-memory scheduling state for this run of the loop;
// it doesn't survive a restart, so every map syncs once immediately whenever
// the process starts.
func (e *Engine) RunLoop(ctx context.Context) error {
	lastSync := make(map[string]time.Time)

	log.Print("running (press Ctrl+C to stop)")

	for {
		wait := e.tick(ctx, lastSync)

		select {
		case <-ctx.Done():
			log.Print("shutting down")
			return nil
		case <-time.After(wait):
		case <-e.wake:
			// A config reload landed (e.g. a map added/edited via /config) —
			// loop back around immediately instead of finishing out the wait
			// computed from the config that's now stale, so a newly added
			// map gets synced right away rather than after whatever sleep
			// happened to already be in flight.
		}
	}
}

// tick runs one pass of RunLoop's schedule: syncing whatever maps are
// currently due (if the engine is configured yet) and returning how long
// RunLoop should sleep before its next tick. lastSync is mutated in place
// for every map synced this tick.
func (e *Engine) tick(ctx context.Context, lastSync map[string]time.Time) time.Duration {
	cfg, _, db := e.Current()
	if db == nil {
		log.Print("waiting for configuration via /config")
		return pollInterval
	}

	now := time.Now()

	due, dueIntervals, wait, haveWait := scheduleTick(cfg.Maps, lastSync, now)
	if !haveWait {
		wait = pollInterval
	}

	if len(due) == 0 {
		return wait
	}

	syncStart := now

	if _, err := e.runSyncMaps(ctx, due); err != nil {
		log.Printf("sync error: %v", err)
	}

	// Fold each just-synced map's own next-due time (syncStart + its
	// Interval) into wait, rather than re-scanning every configured map a
	// second time the way separate due/wait passes used to: this is the
	// only part of the schedule that actually changed by syncing, so it's
	// the only part recomputed here. completed (rather than syncStart) is
	// what remaining is measured against, so a slow sync doesn't shorten
	// the map's next interval — its next due time stays pinned to
	// syncStart+Interval either way.
	completed := time.Now()

	for id := range due {
		lastSync[id] = syncStart

		interval, ok := dueIntervals[id]
		if !ok {
			continue
		}

		remaining := max(interval-completed.Sub(syncStart), 0)
		if !haveWait || remaining < wait {
			wait, haveWait = remaining, true
		}
	}

	return wait
}

// scheduleTick computes, in one pass over maps, which map IDs are due to
// sync right now (a disabled map is skipped entirely — never due, never
// factored into otherWait, and its lastSync entry, if any from before it was
// disabled, is simply left stale; a map with no lastSync entry yet is
// always due, once; after that, a map with a positive Interval is due again
// once that much time has passed since its last sync, while a map with no
// Interval is never due again automatically) together with otherWait/
// haveWait: the shortest remaining time until any *not-due* map with a
// positive Interval next comes due (haveWait is false if there's no such
// map — nothing configured yet, every configured map is one-shot, disabled,
// or already due, or none has synced yet).
//
// dueIntervals carries the positive Interval of every due map, so RunLoop
// can fold each just-synced map's fresh next-due time into otherWait after
// syncing without a second pass over every configured map — mirroring what
// the former separate dueMaps/nextWake functions computed in two full
// passes (nextWake's, run after sync, always re-scanned every map — due and
// not — a second time).
func scheduleTick(maps []config.MapTarget, lastSync map[string]time.Time, now time.Time) (
	due map[string]struct{}, dueIntervals map[string]time.Duration, otherWait time.Duration, haveWait bool,
) {
	for _, m := range maps {
		if m.Disabled {
			continue
		}

		last, seen := lastSync[m.ID]
		interval := m.SyncInterval()

		switch {
		case !seen, interval > 0 && now.Sub(last) >= interval:
			if due == nil {
				due = make(map[string]struct{}, len(maps))
			}

			due[m.ID] = struct{}{}

			if interval > 0 {
				if dueIntervals == nil {
					dueIntervals = make(map[string]time.Duration, len(maps))
				}

				dueIntervals[m.ID] = interval
			}
		case interval > 0:
			remaining := interval - now.Sub(last)
			if !haveWait || remaining < otherWait {
				otherWait, haveWait = remaining, true
			}
		}
	}

	return due, dueIntervals, otherWait, haveWait
}
