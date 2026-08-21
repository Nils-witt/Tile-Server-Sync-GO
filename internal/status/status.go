// Package status tracks in-memory sync status and recent log output for the
// web server in internal/webserver to render.
package status

import (
	"sync"
	"time"
)

// maxLogLines caps how many recent log lines are kept in memory.
const maxLogLines = 500

// MapVersionResult is the outcome of syncing one configured map/version
// pair, keyed by MapID+Version.
type MapVersionResult struct {
	MapID   string
	Version string
	Synced  int
	Err     string
	At      time.Time
}

// Snapshot is a point-in-time, read-only copy of the recorder's state.
type Snapshot struct {
	StartedAt   time.Time
	Runs        int
	LastRunAt   time.Time
	LastRunErr  string
	TotalSynced int
	Results     []MapVersionResult
	Logs        []string
}

// Recorder tracks sync run history and recent log lines. The zero value is
// not usable; construct one with New. A Recorder is also an io.Writer, so it
// can be handed to log.SetOutput (typically via io.MultiWriter, alongside
// the process's normal log destination) to capture log lines for display.
type Recorder struct {
	mu sync.Mutex

	startedAt time.Time
	runs      int
	lastRunAt time.Time
	lastErr   string
	total     int

	// results holds the most recent outcome per "mapID/version" key, plus
	// order to keep the display stable across runs.
	results map[string]MapVersionResult
	order   []string

	logs []string
}

// New returns a Recorder with StartedAt set to now.
func New() *Recorder {
	return &Recorder{
		startedAt: time.Now(),
		results:   make(map[string]MapVersionResult),
	}
}

// RecordMapVersion records the outcome of syncing one map/version pair.
// err may be nil.
func (r *Recorder) RecordMapVersion(mapID, version string, synced int, err error) {
	res := MapVersionResult{MapID: mapID, Version: version, Synced: synced, At: time.Now()}
	if err != nil {
		res.Err = err.Error()
	}

	key := mapID + "/" + version

	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.results[key]; !exists {
		r.order = append(r.order, key)
	}

	r.results[key] = res
}

// RecordRun records the completion of a full syncAll run: runErr is the
// error returned by syncAll itself (nil on success), and totalSynced is the
// number of objects synced across every map/version in the run.
func (r *Recorder) RecordRun(totalSynced int, runErr error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.runs++
	r.lastRunAt = time.Now()
	r.total += totalSynced

	if runErr != nil {
		r.lastErr = runErr.Error()
	} else {
		r.lastErr = ""
	}
}

// Write implements io.Writer, appending each line in p to the in-memory log
// buffer. It never fails.
func (r *Recorder) Write(p []byte) (int, error) {
	line := string(p)

	r.mu.Lock()
	defer r.mu.Unlock()

	r.logs = append(r.logs, line)

	if excess := len(r.logs) - maxLogLines; excess > 0 {
		r.logs = r.logs[excess:]
	}

	return len(p), nil
}

// Snapshot returns a copy of the current state, safe to read without
// further locking.
func (r *Recorder) Snapshot() Snapshot {
	r.mu.Lock()
	defer r.mu.Unlock()

	results := make([]MapVersionResult, len(r.order))
	for i, key := range r.order {
		results[i] = r.results[key]
	}

	logs := make([]string, len(r.logs))
	copy(logs, r.logs)

	return Snapshot{
		StartedAt:   r.startedAt,
		Runs:        r.runs,
		LastRunAt:   r.lastRunAt,
		LastRunErr:  r.lastErr,
		TotalSynced: r.total,
		Results:     results,
		Logs:        logs,
	}
}
