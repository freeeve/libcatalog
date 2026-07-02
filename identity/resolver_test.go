package identity

import "testing"

func TestResolveMintsThenClusters(t *testing.T) {
	r := NewResolver()

	// The ebook: nothing known yet, so both ids are minted.
	a := r.Resolve(Record{
		ProviderKeys: []string{"overdrive:1", "isbn:AAA"},
		Author:       "Byron, Grace", Title: "Herculine", Lang: "eng",
	})
	if !a.MintedInstance || !a.MintedWork {
		t.Fatalf("first record should mint both ids: %+v", a)
	}

	// The audiobook: same work (author+title+lang), different provider id and ISBN
	// -> a new Instance that clusters onto the same Work.
	b := r.Resolve(Record{
		ProviderKeys: []string{"overdrive:2", "isbn:BBB"},
		Author:       "Byron, Grace", Title: "Herculine", Lang: "eng",
	})
	if b.InstanceID == a.InstanceID {
		t.Error("distinct editions must be distinct Instances")
	}
	if b.WorkID != a.WorkID {
		t.Errorf("same work should cluster: %s vs %s", b.WorkID, a.WorkID)
	}
	if !b.MintedInstance || b.MintedWork {
		t.Errorf("audiobook mints an Instance but reuses the Work: %+v", b)
	}
}

func TestResolveByISBNAcrossProviders(t *testing.T) {
	r := NewResolver()
	a := r.Resolve(Record{ProviderKeys: []string{"overdrive:1", "isbn:AAA"}, Author: "A", Title: "T", Lang: "eng"})
	// A different provider id but the same ISBN is the same Instance (§9 merge).
	b := r.Resolve(Record{ProviderKeys: []string{"hoopla:9", "isbn:AAA"}, Author: "A", Title: "T", Lang: "eng"})
	if b.InstanceID != a.InstanceID {
		t.Errorf("same ISBN should resolve to the same Instance: %s vs %s", b.InstanceID, a.InstanceID)
	}
	if b.MintedInstance {
		t.Error("ISBN-matched record should not mint a new Instance")
	}
}

// TestReingestStable simulates a second ingest seeded from the first ingest's
// committed identity: the same feed must resolve to identical ids with no minting.
func TestReingestStable(t *testing.T) {
	rec := Record{ProviderKeys: []string{"overdrive:1", "isbn:AAA"}, Author: "A", Title: "T", Lang: "eng"}

	first := NewResolver()
	a := first.Resolve(rec)

	second := NewResolver()
	second.SeedInstance(a.InstanceID, a.WorkID, rec.ProviderKeys)
	second.SeedWorkKey(WorkKey(rec.Author, rec.Title, rec.Lang), a.WorkID)

	b := second.Resolve(rec)
	if b.InstanceID != a.InstanceID || b.WorkID != a.WorkID {
		t.Errorf("re-ingest churned ids: %+v vs %+v", b, a)
	}
	if b.MintedInstance || b.MintedWork {
		t.Errorf("re-ingest should mint nothing: %+v", b)
	}
}

func TestEditorialMergeOverrides(t *testing.T) {
	r := NewResolver()
	a := r.Resolve(Record{ProviderKeys: []string{"overdrive:1"}, Author: "A", Title: "T", Lang: "eng"})
	// A curator merges a's Work into another Work id.
	r.SeedMerge(a.WorkID, "wcanonical")
	b := r.Resolve(Record{ProviderKeys: []string{"overdrive:1"}, Author: "A", Title: "T", Lang: "eng"})
	if b.WorkID != "wcanonical" {
		t.Errorf("merge should override: got %s, want wcanonical", b.WorkID)
	}
}

func TestConflictSurfaced(t *testing.T) {
	r := NewResolver()
	r.Resolve(Record{ProviderKeys: []string{"isbn:AAA"}, Author: "A", Title: "T", Lang: "eng"})
	r.Resolve(Record{ProviderKeys: []string{"isbn:BBB"}, Author: "A", Title: "T2", Lang: "eng"})
	// A record whose two keys already point at two different Instances is a conflict.
	r.Resolve(Record{ProviderKeys: []string{"isbn:AAA", "isbn:BBB"}, Author: "A", Title: "T", Lang: "eng"})
	if len(r.Conflicts()) == 0 {
		t.Error("expected a provider-key conflict to be surfaced")
	}
}

// TestSplitPinOverridesCluster checks that an editorial split pin (tasks/001)
// assigns an Instance to its pinned Work even though the existing instance->work
// link and the computed key would cluster it elsewhere.
func TestSplitPinOverridesCluster(t *testing.T) {
	r := NewResolver()
	r.SeedInstance("i1", "wshared", []string{ProviderKey(SchemeISBN, "111")})
	r.SeedInstance("i2", "wshared", []string{ProviderKey(SchemeISBN, "222")})
	r.SeedWorkKey(WorkKey("A", "T", "eng"), "wshared")
	r.SeedPin("i2", "wnew")

	rec := func(isbn string) Record {
		return Record{ProviderKeys: []string{ProviderKey(SchemeISBN, isbn)}, Author: "A", Title: "T", Lang: "eng"}
	}
	if a := r.Resolve(rec("111")); a.WorkID != "wshared" {
		t.Errorf("unpinned instance resolved to %s, want wshared", a.WorkID)
	}
	if a := r.Resolve(rec("222")); a.WorkID != "wnew" {
		t.Errorf("pinned instance resolved to %s, want wnew (pin ignored)", a.WorkID)
	}
}
