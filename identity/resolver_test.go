package identity

import "testing"

// TestWorkForProviderKeyFollowsMerge is the seam: a feed cluster-merge is
// translated to Work ids through the resolver's prior-grain state and folded via
// SeedMerge, so a record naming the retired cluster resolves to the survivor.
func TestWorkForProviderKeyFollowsMerge(t *testing.T) {
	r := NewResolver()
	// Two prior clusters, as seeded from prior grains (SeedResolver).
	r.SeedInstance("i1", "w1", []string{"id:coll:1"})
	r.SeedInstance("i2", "w2", []string{"id:coll:2"})

	if w, ok := r.WorkForProviderKey("id:coll:2"); !ok || w != "w2" {
		t.Fatalf("WorkForProviderKey(coll:2) = %q,%v; want w2,true", w, ok)
	}
	if _, ok := r.WorkForProviderKey("id:coll:unknown"); ok {
		t.Fatal("an unknown provider key must return false (no prior grain), so its merge is skipped")
	}

	// The feed folds coll:2 into coll:1: translate + SeedMerge (what cluster() does).
	from, okF := r.WorkForProviderKey("id:coll:2")
	to, okT := r.WorkForProviderKey("id:coll:1")
	if !okF || !okT {
		t.Fatal("both sides of a real merge should resolve from prior grains")
	}
	r.SeedMerge(from, to)

	if w, ok := r.WorkForProviderKey("id:coll:2"); !ok || w != "w1" {
		t.Fatalf("after merge, coll:2 resolves to %q,%v; want the survivor w1,true", w, ok)
	}
}

// TestWorkForProviderKeyFormatBucketFallback is the fix: a feed merge
// names the bare cluster key "coll:N", but a single-format cluster's grain indexes
// only the format-suffixed instance key "coll:N:ebook". The bare key must resolve
// through its format bucket so the merge fires instead of being skipped.
func TestWorkForProviderKeyFormatBucketFallback(t *testing.T) {
	r := NewResolver()
	// Retired single-format cluster: only the suffixed key is indexed.
	r.SeedInstance("i-ebook", "wretired", []string{"id:coll:51812:ebook"})
	// Survivor multi-format cluster: bare + suffixed (has the bare key already).
	r.SeedInstance("i-multi", "wsurv", []string{"id:coll:14", "id:coll:14:ebook", "id:coll:14:physical"})

	// The bare key of the single-format cluster now resolves via its bucket.
	if w, ok := r.WorkForProviderKey("id:coll:51812"); !ok || w != "wretired" {
		t.Fatalf("bare key of a single-format cluster = %q,%v; want wretired,true", w, ok)
	}
	// The survivor's bare key still resolves exactly.
	if w, ok := r.WorkForProviderKey("id:coll:14"); !ok || w != "wsurv" {
		t.Fatalf("survivor bare key = %q,%v; want wsurv,true", w, ok)
	}
	// A numeric-prefix sibling must NOT match via the fallback (the trailing colon
	// guards it): coll:5181 has no bucket of its own.
	if _, ok := r.WorkForProviderKey("id:coll:5181"); ok {
		t.Fatal("id:coll:5181 must not match id:coll:51812:ebook via prefix")
	}

	// End to end: the feed folds the retired cluster into the survivor by bare key.
	from, okF := r.WorkForProviderKey("id:coll:51812")
	to, okT := r.WorkForProviderKey("id:coll:14")
	if !okF || !okT {
		t.Fatal("both sides of the merge should now resolve")
	}
	r.SeedMerge(from, to)
	if w, _ := r.WorkForProviderKey("id:coll:51812"); w != "wsurv" {
		t.Errorf("after merge the retired cluster resolves to %q; want the survivor wsurv", w)
	}
}

// TestWorkForProviderKeyAmbiguousBucketsSkip checks the safety guard: if a bare
// key's format buckets resolve to different Works (a cluster already split across
// Works), the fallback returns nothing rather than guessing.
func TestWorkForProviderKeyAmbiguousBucketsSkip(t *testing.T) {
	r := NewResolver()
	r.SeedInstance("i-a", "wA", []string{"id:coll:9:ebook"})
	r.SeedInstance("i-b", "wB", []string{"id:coll:9:physical"})
	if w, ok := r.WorkForProviderKey("id:coll:9"); ok {
		t.Fatalf("ambiguous buckets must not resolve, got %q", w)
	}
}

// TestMergesReturnsAllApplied checks that Merges() surfaces every applied merge
// (editorial and feed-seeded alike) in deterministic order -- the set the
// retirement pass diffs on.
func TestMergesReturnsAllApplied(t *testing.T) {
	r := NewResolver()
	r.SeedMerge("wc", "wd")
	r.SeedMerge("wa", "wb")
	ms := r.Merges()
	if len(ms) != 2 {
		t.Fatalf("Merges() = %+v, want 2", ms)
	}
	if ms[0].From != "wa" || ms[0].To != "wb" || ms[1].From != "wc" || ms[1].To != "wd" {
		t.Errorf("Merges() not sorted by From: %+v", ms)
	}
}

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

// TestSplitPinOverridesCluster checks that an editorial split pin
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

// TestConflictingPinIsReportedNotSilentlyDropped covers the resolver half of
// two pins for one instance (a split written twice before the endpoint was
// idempotent, or a hand-edited grain) must not let quad sort order silently decide the
// winner. The first pin seen wins deterministically, the second is surfaced as a
// conflict, and the discarded Work id is not reserved.
func TestConflictingPinIsReportedNotSilentlyDropped(t *testing.T) {
	r := NewResolver()
	r.SeedPin("i2", "wfirst")
	r.SeedPin("i2", "wsecond")

	if got := r.pinByInst["i2"]; got != "wfirst" {
		t.Errorf("pin = %s, want wfirst (first seen wins deterministically)", got)
	}
	if len(r.Conflicts()) != 1 {
		t.Fatalf("conflicts = %v, want one entry naming the double pin", r.Conflicts())
	}
	// The discarded id denotes nothing; reserving it would burn it out of the space.
	if r.usedWork["wsecond"] {
		t.Error("the discarded Work id was reserved")
	}
	// A repeated identical pin is not a conflict.
	r.SeedPin("i2", "wfirst")
	if len(r.Conflicts()) != 1 {
		t.Errorf("an identical re-pin was counted as a conflict: %v", r.Conflicts())
	}
}

// TestEmptyKeyNeverClusters checks that records with no main title (an empty
// access-point key) each mint their own Work instead of all
// clustering onto the first one, in both fresh-resolve and seeded form.
func TestEmptyKeyNeverClusters(t *testing.T) {
	r := NewResolver()
	a := r.Resolve(Record{ProviderKeys: []string{"overdrive:1"}})
	b := r.Resolve(Record{ProviderKeys: []string{"overdrive:2"}, Author: "A", Lang: "eng"})
	if a.WorkID == b.WorkID {
		t.Errorf("title-less records must not cluster: both got %s", a.WorkID)
	}
	if !b.MintedWork {
		t.Errorf("title-less record should mint its own Work: %+v", b)
	}

	// Seeding an empty committed key must not create a cluster target either.
	seeded := NewResolver()
	seeded.SeedWorkKey(WorkKey("", "", ""), "wempty0000")
	c := seeded.Resolve(Record{ProviderKeys: []string{"overdrive:3"}})
	if c.WorkID == "wempty0000" {
		t.Error("empty seeded key must not attract title-less records")
	}
}
