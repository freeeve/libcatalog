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
