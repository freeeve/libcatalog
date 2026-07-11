package workindex

import (
	"context"
	"fmt"
	"iter"
	"sync/atomic"
	"testing"

	"github.com/freeeve/libcat/bibframe"
	"github.com/freeeve/libcat/storage/blob"
)

// countingStore wraps a Store and counts Get and List calls, so tests can
// assert the index's central promise: no per-request corpus reads.
type countingStore struct {
	blob.Store
	gets, lists atomic.Int64
}

func (c *countingStore) Get(ctx context.Context, path string) ([]byte, string, error) {
	c.gets.Add(1)
	return c.Store.Get(ctx, path)
}

func (c *countingStore) List(ctx context.Context, prefix string) iter.Seq2[blob.Entry, error] {
	c.lists.Add(1)
	return c.Store.List(ctx, prefix)
}

// grain renders a minimal work grain with the identity signals the index
// scans (cluster key, ISBN provider key) plus one held item barcode.
func grain(workID, title, author, isbn, barcode string) []byte {
	g := fmt.Appendf(nil, `<#%[1]sWork> <http://www.w3.org/1999/02/22-rdf-syntax-ns#type> <http://id.loc.gov/ontologies/bibframe/Work> <feed:overdrive> .
<#%[1]sWork> <http://id.loc.gov/ontologies/bibframe/title> _:t <feed:overdrive> .
<#%[1]sWork> <http://id.loc.gov/ontologies/bibframe/language> <http://id.loc.gov/vocabulary/languages/eng> <feed:overdrive> .
<#%[1]sWork> <http://id.loc.gov/ontologies/bibframe/contribution> _:c <feed:overdrive> .
_:t <http://id.loc.gov/ontologies/bibframe/mainTitle> "%[2]s" <feed:overdrive> .
_:c <http://www.w3.org/1999/02/22-rdf-syntax-ns#type> <http://id.loc.gov/ontologies/bflc/PrimaryContribution> <feed:overdrive> .
_:c <http://id.loc.gov/ontologies/bibframe/agent> _:ag <feed:overdrive> .
_:ag <http://www.w3.org/2000/01/rdf-schema#label> "%[3]s" <feed:overdrive> .
<#%[1]siInstance> <http://www.w3.org/1999/02/22-rdf-syntax-ns#type> <http://id.loc.gov/ontologies/bibframe/Instance> <feed:overdrive> .
<#%[1]siInstance> <http://id.loc.gov/ontologies/bibframe/instanceOf> <#%[1]sWork> <feed:overdrive> .
<#%[1]siInstance> <http://id.loc.gov/ontologies/bibframe/identifiedBy> _:a <feed:overdrive> .
_:a <http://www.w3.org/1999/02/22-rdf-syntax-ns#type> <http://id.loc.gov/ontologies/bibframe/Isbn> <feed:overdrive> .
_:a <http://www.w3.org/1999/02/22-rdf-syntax-ns#value> "%[4]s" <feed:overdrive> .
`, workID, title, author, isbn)
	if barcode != "" {
		g = fmt.Appendf(g, `<#%[1]siInstance> <http://id.loc.gov/ontologies/bibframe/hasItem> _:it <editorial:items> .
_:it <%[2]s> "%[3]s" <editorial:items> .
`, workID, bibframe.PredBarcode, barcode)
	}
	return g
}

func seed(t *testing.T, bs blob.Store, workID string, g []byte) string {
	t.Helper()
	etag, err := bs.Put(t.Context(), bibframe.GrainPath(workID), g, blob.PutOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return etag
}

func TestIndexLookupsAndIOBudget(t *testing.T) {
	ctx := t.Context()
	cs := &countingStore{Store: blob.NewMem()}
	seed(t, cs, "w1", grain("w1", "The Left Hand of Darkness", "Le Guin, Ursula K.", "9780441478125", "B-0001"))
	seed(t, cs, "w2", grain("w2", "The Dispossessed", "Le Guin, Ursula K.", "9780061054884", "B-0002"))
	seed(t, cs, "w3", grain("w3", "The Dispossessed", "Le Guin, Ursula K.", "9780061054885", ""))
	ix := New(cs, "data/works/")

	// First read pays the full build: one List, one Get per grain.
	owners, err := ix.ProviderOwners(ctx, "isbn:9780441478125")
	if err != nil {
		t.Fatal(err)
	}
	if len(owners) != 1 || owners[0].WorkID != "w1" || owners[0].Path != bibframe.GrainPath("w1") {
		t.Fatalf("provider owners = %+v", owners)
	}
	if got := cs.gets.Load(); got != 3 {
		t.Fatalf("build gets = %d, want 3", got)
	}

	// Every further read within the TTL costs zero blob operations.
	cs.gets.Store(0)
	cs.lists.Store(0)
	if owners, _ = ix.ClusterOwners(ctx, dispossessedKey(t, ix)); len(owners) != 2 {
		t.Fatalf("cluster owners = %+v", owners)
	}
	groups, err := ix.DuplicateGroups(ctx)
	if err != nil || len(groups) != 1 {
		t.Fatalf("groups = %v, %v", groups, err)
	}
	for _, ids := range groups {
		if len(ids) != 2 || ids[0] != "w2" || ids[1] != "w3" {
			t.Fatalf("group members = %v", ids)
		}
	}
	taken, err := ix.Barcodes(ctx)
	if err != nil || !taken["B-0001"] || !taken["B-0002"] || len(taken) != 2 {
		t.Fatalf("barcodes = %v, %v", taken, err)
	}
	sums, err := ix.Summaries(ctx)
	if err != nil || len(sums) != 3 || sums[0].WorkID != "w1" || sums[0].Title != "The Left Hand of Darkness" {
		t.Fatalf("summaries = %+v, %v", sums, err)
	}
	if cs.gets.Load() != 0 || cs.lists.Load() != 0 {
		t.Fatalf("cached reads did I/O: %d gets, %d lists", cs.gets.Load(), cs.lists.Load())
	}
}

// dispossessedKey recovers the shared cluster key from the duplicate groups,
// so the test does not re-implement identity.WorkKey.
func dispossessedKey(t *testing.T, ix *Index) string {
	t.Helper()
	groups, err := ix.DuplicateGroups(t.Context())
	if err != nil || len(groups) != 1 {
		t.Fatalf("groups = %v, %v", groups, err)
	}
	for key := range groups {
		return key
	}
	return ""
}

func TestApplyKeepsIndexExactWithoutRescan(t *testing.T) {
	ctx := t.Context()
	cs := &countingStore{Store: blob.NewMem()}
	seed(t, cs, "w1", grain("w1", "A Wizard of Earthsea", "Le Guin, Ursula K.", "9780547773742", "B-0001"))
	ix := New(cs, "data/works/")
	if _, err := ix.Summaries(ctx); err != nil {
		t.Fatal(err)
	}

	// A write pushed in via Apply is visible at once, with no blob reads.
	updated := grain("w1", "A Wizard of Earthsea", "Le Guin, Ursula K.", "9780547773742", "B-0009")
	etag := seed(t, cs, "w1", updated)
	cs.gets.Store(0)
	cs.lists.Store(0)
	ix.Apply(bibframe.GrainPath("w1"), etag, updated)
	taken, err := ix.Barcodes(ctx)
	if err != nil || !taken["B-0009"] || taken["B-0001"] {
		t.Fatalf("barcodes after Apply = %v, %v", taken, err)
	}
	if cs.gets.Load() != 0 || cs.lists.Load() != 0 {
		t.Fatalf("Apply readback did I/O: %d gets, %d lists", cs.gets.Load(), cs.lists.Load())
	}
}

// TestDuplicateBarcodes is the tasks/270 report: a barcode held by more than one
// item across the corpus is surfaced with the works holding it; a barcode on one
// item is not.
func TestDuplicateBarcodes(t *testing.T) {
	ctx := t.Context()
	cs := &countingStore{Store: blob.NewMem()}
	seed(t, cs, "w1", grain("w1", "A", "X", "111", "DUP-1"))
	seed(t, cs, "w2", grain("w2", "B", "Y", "222", "DUP-1")) // same barcode as w1
	seed(t, cs, "w3", grain("w3", "C", "Z", "333", "UNIQ-1"))
	ix := New(cs, "data/works/")

	dups, err := ix.DuplicateBarcodes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(dups) != 1 {
		t.Fatalf("duplicates = %+v, want exactly the shared barcode", dups)
	}
	d := dups[0]
	if d.Barcode != "DUP-1" || d.Count != 2 || len(d.WorkIDs) != 2 || d.WorkIDs[0] != "w1" || d.WorkIDs[1] != "w2" {
		t.Fatalf("duplicate = %+v, want DUP-1 held by w1 and w2", d)
	}
}

// TestDuplicateGroupsExcludesHiddenWorks is the tasks/348 fix: a tombstoned or
// suppressed work is retired, so it is not a merge candidate and drops out of the
// duplicates report -- mirroring DuplicateBarcodes' live-only filter. A group left
// with fewer than two live works disappears entirely.
func TestDuplicateGroupsExcludesHiddenWorks(t *testing.T) {
	ctx := t.Context()
	cs := &countingStore{Store: blob.NewMem()}
	// Three works that cluster together (same title + author).
	seed(t, cs, "w1", grain("w1", "Same Title", "Same Author", "111", ""))
	seed(t, cs, "w2", grain("w2", "Same Title", "Same Author", "222", ""))
	seed(t, cs, "w3", grain("w3", "Same Title", "Same Author", "333", ""))
	ix := New(cs, "data/works/")

	var key string
	groups, err := ix.DuplicateGroups(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for k, ids := range groups {
		if len(ids) == 3 {
			key = k
		}
	}
	if key == "" {
		t.Fatalf("no 3-work group formed: %+v", groups)
	}
	has := func(ids []string, id string) bool {
		for _, x := range ids {
			if x == id {
				return true
			}
		}
		return false
	}

	// Tombstone w1 -> it drops out; the group keeps its two live works.
	tomb, err := bibframe.SetTombstone(grain("w1", "Same Title", "Same Author", "111", ""), "w1", "w2")
	if err != nil {
		t.Fatal(err)
	}
	ix.Apply(bibframe.GrainPath("w1"), seed(t, cs, "w1", tomb), tomb)
	groups, _ = ix.DuplicateGroups(ctx)
	if ids := groups[key]; len(ids) != 2 || has(ids, "w1") {
		t.Fatalf("after tombstone, group = %v, want two live works without w1", ids)
	}

	// Tombstone w2 too -> only w3 live, so the group falls below 2 and vanishes.
	tomb2, err := bibframe.SetTombstone(grain("w2", "Same Title", "Same Author", "222", ""), "w2", "w3")
	if err != nil {
		t.Fatal(err)
	}
	ix.Apply(bibframe.GrainPath("w2"), seed(t, cs, "w2", tomb2), tomb2)
	groups, _ = ix.DuplicateGroups(ctx)
	if ids, ok := groups[key]; ok {
		t.Fatalf("a group with fewer than two live works is still reported: %v", ids)
	}
}

// TestBarcodeHeldByOther is the tasks/347 write-time check: a barcode on a live
// item of another instance is a collision; the holding instance itself is not
// (a re-save); and a suppressed work's barcode is not live, so it frees up.
func TestBarcodeHeldByOther(t *testing.T) {
	ctx := t.Context()
	cs := &countingStore{Store: blob.NewMem()}
	seed(t, cs, "w1", grain("w1", "A", "X", "111", "SHARED")) // barcode on instance w1i
	ix := New(cs, "data/works/")

	held, by, err := ix.BarcodeHeldByOther(ctx, "SHARED", "w2", "w2i")
	if err != nil {
		t.Fatal(err)
	}
	if !held || by.WorkID != "w1" {
		t.Fatalf("held=%v by=%+v, want held by w1", held, by)
	}
	// The instance already holding it is excluded -- re-saving is not a collision.
	if h, _, _ := ix.BarcodeHeldByOther(ctx, "SHARED", "w1", "w1i"); h {
		t.Fatal("re-saving the holding instance flagged as a collision")
	}
	// An unheld barcode is free.
	if h, _, _ := ix.BarcodeHeldByOther(ctx, "UNHELD", "w2", "w2i"); h {
		t.Fatal("an unheld barcode reported as held")
	}

	// Suppressing w1 makes its barcode non-live, so it no longer blocks.
	suppressed, err := bibframe.SetSuppressed(grain("w1", "A", "X", "111", "SHARED"), "w1", true)
	if err != nil {
		t.Fatal(err)
	}
	etag := seed(t, cs, "w1", suppressed)
	ix.Apply(bibframe.GrainPath("w1"), etag, suppressed)
	if h, _, _ := ix.BarcodeHeldByOther(ctx, "SHARED", "w2", "w2i"); h {
		t.Fatal("a suppressed work's barcode still blocked a new assignment")
	}
}

func TestRefreshDiffsByETag(t *testing.T) {
	ctx := t.Context()
	cs := &countingStore{Store: blob.NewMem()}
	seed(t, cs, "w1", grain("w1", "The Left Hand of Darkness", "Le Guin, Ursula K.", "9780441478125", ""))
	seed(t, cs, "w2", grain("w2", "The Dispossessed", "Le Guin, Ursula K.", "9780061054884", ""))
	ix := New(cs, "data/works/")
	if _, err := ix.Summaries(ctx); err != nil {
		t.Fatal(err)
	}

	// Change one grain, add one, delete one behind the index's back, then
	// expire the TTL: the refresh re-reads only the changed and new grains.
	seed(t, cs, "w1", grain("w1", "The Left Hand of Darkness (rev)", "Le Guin, Ursula K.", "9780441478125", ""))
	seed(t, cs, "w3", grain("w3", "Always Coming Home", "Le Guin, Ursula K.", "9780520227354", ""))
	if err := cs.Delete(ctx, bibframe.GrainPath("w2")); err != nil {
		t.Fatal(err)
	}
	ix.at = ix.at.Add(-2 * DefaultTTL)
	cs.gets.Store(0)
	sums, err := ix.Summaries(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(sums) != 2 || sums[0].Title != "The Left Hand of Darkness (rev)" || sums[1].WorkID != "w3" {
		t.Fatalf("summaries after refresh = %+v", sums)
	}
	if got := cs.gets.Load(); got != 2 {
		t.Fatalf("refresh gets = %d, want 2 (changed + new only)", got)
	}
}
