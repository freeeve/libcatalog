package workindex

import (
	"context"
	"fmt"
	"iter"
	"testing"

	"github.com/freeeve/libcat/bibframe"
	"github.com/freeeve/libcat/storage/blob"
)

// TestSnapshotRoundTrip: a saved snapshot lets a fresh index serve the same
// summaries, barcodes, and provider lookups without re-reading any grain.
func TestSnapshotRoundTrip(t *testing.T) {
	ctx := t.Context()
	cs := &countingStore{Store: blob.NewMem()}
	seed(t, cs, "w1", grain("w1", "A Wizard of Earthsea", "Le Guin, Ursula K.", "9780547773742", "B-0001"))
	seed(t, cs, "w2", grain("w2", "The Tombs of Atuan", "Le Guin, Ursula K.", "9780689845369", "B-0002"))
	src := New(cs, "data/works/")
	want, err := src.Summaries(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := src.Save(ctx); err != nil {
		t.Fatal(err)
	}

	dst := New(cs, "data/works/")
	if err := dst.LoadSnapshot(ctx); err != nil {
		t.Fatal(err)
	}
	dst.feedActive = false // isolate snapshot load; the feed has its own tests
	cs.gets.Store(0)
	cs.lists.Store(0)
	got, err := dst.Summaries(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(want) || got[0].WorkID != want[0].WorkID || got[1].Title != want[1].Title {
		t.Fatalf("summaries after load = %+v, want %+v", got, want)
	}
	// The reconcile Lists once but re-reads zero grains: the snapshot's ETags
	// all match, so the corpus scan is skipped.
	if cs.gets.Load() != 0 {
		t.Fatalf("load served %d grain gets, want 0 (snapshot only)", cs.gets.Load())
	}
	taken, _ := dst.Barcodes(ctx)
	if !taken["B-0001"] || !taken["B-0002"] {
		t.Fatalf("barcodes after load = %v", taken)
	}
	owners, _ := dst.ProviderOwners(ctx, "isbn:9780547773742")
	if len(owners) != 1 || owners[0].WorkID != "w1" {
		t.Fatalf("provider owners after load = %+v", owners)
	}
}

// TestSnapshotStaleReconciles: an out-of-date snapshot is still correct -- the
// ETag diff re-reads only the grains changed since it, and drops deletions.
func TestSnapshotStaleReconciles(t *testing.T) {
	ctx := t.Context()
	cs := &countingStore{Store: blob.NewMem()}
	seed(t, cs, "w1", grain("w1", "The Left Hand of Darkness", "Le Guin, Ursula K.", "9780441478125", ""))
	seed(t, cs, "w2", grain("w2", "The Dispossessed", "Le Guin, Ursula K.", "9780061054884", ""))
	src := New(cs, "data/works/")
	if _, err := src.Summaries(ctx); err != nil {
		t.Fatal(err)
	}
	if err := src.Save(ctx); err != nil {
		t.Fatal(err)
	}

	// Mutate the corpus after the snapshot: change w1, add w3, delete w2.
	seed(t, cs, "w1", grain("w1", "The Left Hand of Darkness (rev)", "Le Guin, Ursula K.", "9780441478125", ""))
	seed(t, cs, "w3", grain("w3", "Always Coming Home", "Le Guin, Ursula K.", "9780520227354", ""))
	if err := cs.Delete(ctx, bibframe.GrainPath("w2")); err != nil {
		t.Fatal(err)
	}

	dst := New(cs, "data/works/")
	if err := dst.LoadSnapshot(ctx); err != nil {
		t.Fatal(err)
	}
	dst.feedActive = false // isolate snapshot reconcile; the feed has its own tests
	cs.gets.Store(0)
	sums, err := dst.Summaries(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(sums) != 2 || sums[0].Title != "The Left Hand of Darkness (rev)" || sums[1].WorkID != "w3" {
		t.Fatalf("summaries after stale load = %+v", sums)
	}
	if got := cs.gets.Load(); got != 2 {
		t.Fatalf("reconcile gets = %d, want 2 (changed + new only)", got)
	}
}

// TestSnapshotMissing: no snapshot is not an error; the index warms from the
// store as before.
func TestSnapshotMissing(t *testing.T) {
	ctx := t.Context()
	cs := &countingStore{Store: blob.NewMem()}
	seed(t, cs, "w1", grain("w1", "A Wizard of Earthsea", "Le Guin, Ursula K.", "9780547773742", ""))
	ix := New(cs, "data/works/")
	if err := ix.LoadSnapshot(ctx); err != nil {
		t.Fatalf("missing snapshot should be nil, got %v", err)
	}
	sums, err := ix.Summaries(ctx)
	if err != nil || len(sums) != 1 {
		t.Fatalf("warm from store after missing snapshot = %+v, %v", sums, err)
	}
}

// TestSnapshotCorruptFallsBack: a garbage or wrong-version snapshot errors so
// the caller can fall back to a full scan -- it never corrupts the index.
func TestSnapshotCorruptFallsBack(t *testing.T) {
	ctx := t.Context()
	cs := &countingStore{Store: blob.NewMem()}
	seed(t, cs, "w1", grain("w1", "A Wizard of Earthsea", "Le Guin, Ursula K.", "9780547773742", ""))

	if _, err := cs.Put(ctx, DefaultSnapshotPath, []byte("not gzip"), blob.PutOptions{}); err != nil {
		t.Fatal(err)
	}
	ix := New(cs, "data/works/")
	if err := ix.LoadSnapshot(ctx); err == nil {
		t.Fatal("corrupt snapshot should return an error")
	}
	// The index is untouched, so a normal read still scans the store correctly.
	sums, err := ix.Summaries(ctx)
	if err != nil || len(sums) != 1 || sums[0].WorkID != "w1" {
		t.Fatalf("scan after corrupt snapshot = %+v, %v", sums, err)
	}
}

// etagRewriter presents the wrapped store's content under a different ETag
// scheme -- the failure: a snapshot built via one backend (dir,
// sha256 ETags) serving a store with another (S3, MD5-based ETags).
type etagRewriter struct {
	blob.Store
}

func (r *etagRewriter) Get(ctx context.Context, path string) ([]byte, string, error) {
	data, etag, err := r.Store.Get(ctx, path)
	return data, "other-" + etag, err
}

func (r *etagRewriter) List(ctx context.Context, prefix string) iter.Seq2[blob.Entry, error] {
	return func(yield func(blob.Entry, error) bool) {
		for entry, err := range r.Store.List(ctx, prefix) {
			entry.ETag = "other-" + entry.ETag
			if !yield(entry, err) {
				return
			}
		}
	}
}

// TestSnapshotDriftForeignETagScheme: a snapshot built against a store with a
// different ETag scheme re-fetches everything on the first reconcile, and
// SnapshotDrift reports it so boot can warn. A matching snapshot reports zero.
func TestSnapshotDriftForeignETagScheme(t *testing.T) {
	ctx := t.Context()
	mem := blob.NewMem()
	seed(t, mem, "w1", grain("w1", "A Wizard of Earthsea", "Le Guin, Ursula K.", "9780547773742", ""))
	seed(t, mem, "w2", grain("w2", "The Tombs of Atuan", "Le Guin, Ursula K.", "9780689845369", ""))
	builder := New(mem, "data/works/")
	if _, err := builder.Summaries(ctx); err != nil {
		t.Fatal(err)
	}
	if err := builder.Save(ctx); err != nil {
		t.Fatal(err)
	}

	// Same bytes, different ETag scheme: every primed entry misses.
	foreign := New(&etagRewriter{Store: mem}, "data/works/")
	if err := foreign.LoadSnapshot(ctx); err != nil {
		t.Fatal(err)
	}
	if p, r := foreign.SnapshotDrift(); p != 0 || r != 0 {
		t.Fatalf("drift before reconcile = %d/%d, want 0/0", r, p)
	}
	if err := foreign.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if p, r := foreign.SnapshotDrift(); p != 2 || r != 2 {
		t.Fatalf("foreign-scheme drift = refetched %d of primed %d, want 2 of 2", r, p)
	}

	// The matched-store control: priming costs zero re-fetches.
	native := New(mem, "data/works/")
	if err := native.LoadSnapshot(ctx); err != nil {
		t.Fatal(err)
	}
	if err := native.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if p, r := native.SnapshotDrift(); p != 2 || r != 0 {
		t.Fatalf("native drift = refetched %d of primed %d, want 0 of 2", r, p)
	}
}

// TestWarmScan: the concurrent reconcile matches refreshLocked's semantics --
// unchanged ETags are skipped, changed and new grains re-read, deletions
// dropped -- and a snapshot saved from it primes a zero-Get boot.
func TestWarmScan(t *testing.T) {
	ctx := t.Context()
	cs := &countingStore{Store: blob.NewMem()}
	for i := 1; i <= 5; i++ {
		id := fmt.Sprintf("w%d", i)
		seed(t, cs, id, grain(id, "Title "+id, "Author", fmt.Sprintf("978000000000%d", i), ""))
	}
	ix := New(cs, "data/works/")
	var calls, total int
	if err := ix.WarmScan(ctx, 4, func(done, n int) { calls++; total = n }); err != nil {
		t.Fatal(err)
	}
	if calls != 5 || total != 5 {
		t.Fatalf("progress calls = %d (total %d), want 5 (total 5)", calls, total)
	}
	sums, err := ix.Summaries(ctx)
	if err != nil || len(sums) != 5 {
		t.Fatalf("after warm scan: %d summaries, err %v", len(sums), err)
	}
	if err := ix.Save(ctx); err != nil {
		t.Fatal(err)
	}

	// Mutate: change w1, delete w2, add w6. The second scan re-reads only the
	// changed and new grains.
	seed(t, cs, "w1", grain("w1", "Title w1 revised", "Author", "9780000000001", ""))
	if err := cs.Delete(ctx, bibframe.GrainPath("w2")); err != nil {
		t.Fatal(err)
	}
	seed(t, cs, "w6", grain("w6", "Title w6", "Author", "9780000000006", ""))
	cs.gets.Store(0)
	if err := ix.WarmScan(ctx, 4, nil); err != nil {
		t.Fatal(err)
	}
	if got := cs.gets.Load(); got != 2 {
		t.Fatalf("second warm scan gets = %d, want 2 (changed + new only)", got)
	}
	sums, err = ix.Summaries(ctx)
	if err != nil || len(sums) != 5 {
		t.Fatalf("after mutate + warm scan: %d summaries, err %v", len(sums), err)
	}
	for _, s := range sums {
		if s.WorkID == "w2" {
			t.Fatal("deleted grain w2 still indexed")
		}
		if s.WorkID == "w1" && s.Title != "Title w1 revised" {
			t.Fatalf("w1 title = %q, want the revised one", s.Title)
		}
	}

	// A snapshot saved from the scanned state primes a fresh index with zero
	// grain reads.
	if err := ix.Save(ctx); err != nil {
		t.Fatal(err)
	}
	fresh := New(cs, "data/works/")
	if err := fresh.LoadSnapshot(ctx); err != nil {
		t.Fatal(err)
	}
	fresh.feedActive = false
	cs.gets.Store(0)
	sums, err = fresh.Summaries(ctx)
	if err != nil || len(sums) != 5 {
		t.Fatalf("primed boot: %d summaries, err %v", len(sums), err)
	}
	if got := cs.gets.Load(); got != 0 {
		t.Fatalf("primed boot grain gets = %d, want 0", got)
	}
}
