package suggest

import (
	"testing"
	"time"
)

// The audit sort key was time.RFC3339Nano, which *trims trailing zeros* from
// the fractional second. So a write at .167790000 keys as ".16779Z" and one at
// .167792000 keys as ".167792Z"; descending lexicographic order puts 'Z' (0x5A)
// above '2', and the earlier entry comes back first.
//
// "Newest first" is the whole contract of the audit trail, and a bulk run
// writes several entries inside one microsecond, so this surfaced the moment
// per-record entries existed: a reader taking the first BATCH_EDIT
// row got an arbitrary one.
func TestAuditIsNewestFirstWhenNanosEndInZero(t *testing.T) {
	svc, _ := newService(t)
	base := time.Date(2026, 7, 9, 16, 59, 3, 0, time.UTC)

	// Nanosecond offsets chosen so the trimmed-zero forms interleave badly:
	// 167790000 -> ".16779", 167792000 -> ".167792", 167788000 -> ".167788".
	type write struct {
		nanos int
		work  string
	}
	writes := []write{
		{167777000, "wg1t52gdovldjm"},
		{167788000, "wilv1l65a723g0"},
		{167790000, "wp8ljrjke4qsdm"},
		{167792000, ""}, // the aggregate, written last and therefore newest
	}
	for _, w := range writes {
		writeAt(t, svc, base.Add(time.Duration(w.nanos)), AuditEntry{
			Actor: "lib@example.org", Action: "BATCH_EDIT", WorkID: w.work,
		})
	}

	entries, err := svc.Audit(t.Context(), "2026-07")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != len(writes) {
		t.Fatalf("got %d entries, want %d", len(entries), len(writes))
	}
	// Newest first: the aggregate, written last, must lead.
	if entries[0].WorkID != "" {
		t.Fatalf("first entry is %q, want the aggregate (written last)", entries[0].WorkID)
	}
	for i := 1; i < len(entries); i++ {
		if entries[i-1].At.Before(entries[i].At) {
			t.Fatalf("entry %d (%s) is older than entry %d (%s): the trail is not newest-first",
				i-1, entries[i-1].At.Format(time.RFC3339Nano), i, entries[i].At.Format(time.RFC3339Nano))
		}
	}
}

// The read-side sort repairs the keys already in the store; the key format has
// to be monotonic too, or a future keyed scan (pagination, a range query)
// inherits the same defect with no sort to save it.
func TestAuditSKLayoutIsLexicographicallyMonotonic(t *testing.T) {
	base := time.Date(2026, 7, 9, 16, 59, 3, 0, time.UTC)
	nanos := []int{0, 1, 999, 167777000, 167788000, 167790000, 167792000, 999999999}
	var prev string
	for _, n := range nanos {
		got := base.Add(time.Duration(n)).Format(auditSKLayout)
		if prev != "" && !(prev < got) {
			t.Fatalf("key %q does not sort below %q, so a later write is not a later key", prev, got)
		}
		prev = got
	}
	// The bug, stated as a fact about the format it replaced.
	trimmed := base.Add(167790000).Format(time.RFC3339Nano)
	later := base.Add(167792000).Format(time.RFC3339Nano)
	if trimmed < later {
		t.Fatal("RFC3339Nano no longer trims trailing zeros; this test and the layout constant can go")
	}
}
