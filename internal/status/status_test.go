package status

import "testing"

func TestRemoveMap(t *testing.T) {
	t.Parallel()

	r := New()
	r.RecordMapVersion("a", "1", 1, nil)
	r.RecordMapVersion("b", "1", 2, nil)
	r.RecordMapVersion("a", "current", 3, nil)

	r.RemoveMap("a")

	results := r.Snapshot().Results
	if len(results) != 1 || results[0].MapID != "b" {
		t.Fatalf("results after RemoveMap(a) = %+v, want only map b", results)
	}

	// Re-recording a removed map appends it again rather than reusing stale order.
	r.RecordMapVersion("a", "1", 4, nil)

	results = r.Snapshot().Results
	if len(results) != 2 || results[1].MapID != "a" || results[1].Synced != 4 {
		t.Fatalf("results after re-record = %+v", results)
	}
}
