// the claim that a failed promotion is free to retry rests on
// PromoteTag's own loop, not on any idempotence in the write:
//
//	for _, summary := range summaries {
//	    if !slices.Contains(summary.Tags, promo.Tag) { continue }
//
// A work rewritten on the first attempt had its editorial folk tag retracted, so
// the second attempt passes over it and resumes at the one that failed. These
// tests hold the real publisher to that, with a store that fails partway.
package publish

import (
	"context"
	"errors"
	"iter"
	"strings"
	"testing"

	"github.com/freeeve/libcodex/rdf"

	"github.com/freeeve/libcat/bibframe"
	"github.com/freeeve/libcat/storage/blob"

	"github.com/freeeve/libcat/backend/vocab"
)

// failAfter wraps a blob store and fails the Nth write of a work grain, the way
// one read-only shard does. Writes to other prefixes (the alias grain) pass.
type failAfter struct {
	blob.Store
	after  int // allow this many work-grain writes, then fail
	writes int
	off    bool
}

var errShardReadOnly = errors.New("shard is read-only")

func (f *failAfter) Put(ctx context.Context, path string, data []byte, opts blob.PutOptions) (string, error) {
	if !f.off && strings.HasPrefix(path, "data/works/") {
		f.writes++
		if f.writes > f.after {
			return "", errShardReadOnly
		}
	}
	return f.Store.Put(ctx, path, data, opts)
}
func (f *failAfter) Get(ctx context.Context, p string) ([]byte, string, error) {
	return f.Store.Get(ctx, p)
}
func (f *failAfter) Delete(ctx context.Context, p string) error { return f.Store.Delete(ctx, p) }
func (f *failAfter) List(ctx context.Context, p string) iter.Seq2[blob.Entry, error] {
	return f.Store.List(ctx, p)
}

// tagged reports whether the grain still carries the editorial folk tag.
func tagged(t *testing.T, bs blob.Store, workID string) bool {
	t.Helper()
	g, _, err := bs.Get(context.Background(), bibframe.GrainPath(workID))
	if err != nil {
		t.Fatal(err)
	}
	return strings.Contains(string(g), bibframe.PredTag+">")
}

// seedFolkTaggedWork writes a grain whose ONLY carrier of the tag is the
// editorial folk statement. That is what a cataloger's tag is, and it is the
// shape PromoteTag can skip on a retry: seedTaggedWork also plants a feed-side
// tag, which the promotion deliberately never retracts, so such a work matches
// the loop again on every pass.
func seedFolkTaggedWork(t *testing.T, bs blob.Store, workID, tag string) {
	t.Helper()
	ds := &rdf.Dataset{}
	feed := bibframe.FeedGraph("overdrive")
	ds.Add(rdf.NewIRI(bibframe.WorkIRI(workID)),
		rdf.NewIRI("http://www.w3.org/1999/02/22-rdf-syntax-ns#type"),
		rdf.NewIRI("http://id.loc.gov/ontologies/bibframe/Work"), feed)
	nq, err := ds.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	nq, err = bibframe.ApplyEditorialPatch(nq, bibframe.Patch{Add: []rdf.Quad{bibframe.TagQuad(workID, tag)}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bs.Put(t.Context(), bibframe.GrainPath(workID), nq, blob.PutOptions{}); err != nil {
		t.Fatal(err)
	}
}

// A rewrite that fails on the second work leaves the first one promoted and
// reports the count -- which the handler records, so the queue never says a
// partial rewrite was nothing.
func TestPromoteTagReportsWhatItRewroteBeforeFailing(t *testing.T) {
	pub, grains, queue, _ := newPublisher(t)
	seedTaggedWork(t, grains, "wtagged000001", "queer joy", true)
	seedTaggedWork(t, grains, "wtagged000002", "queer joy", true)
	flaky := &failAfter{Store: grains, after: 1}
	pub.Blob = flaky

	promo, err := queue.ProposePromotion(t.Context(), "queer joy", vocab.TermRef{Scheme: "homosaurus", ID: transURI}, "mod")
	if err != nil {
		t.Fatal(err)
	}
	works, err := pub.PromoteTag(t.Context(), promo, "lib@example.org")
	if !errors.Is(err, errShardReadOnly) {
		t.Fatalf("err = %v, want the shard failure", err)
	}
	if works != 1 {
		t.Fatalf("rewrote %d works before failing, want 1", works)
	}
	// Exactly one of the two landed. Both would mean the fault never fired; none
	// would mean the count is a lie in the other direction.
	first, second := tagged(t, grains, "wtagged000001"), tagged(t, grains, "wtagged000002")
	if first == second {
		t.Fatalf("expected exactly one work rewritten; tagged: w1=%v w2=%v", first, second)
	}
}

// Retrying after the store recovers resumes at the work that failed, because the
// rewritten one no longer carries the tag. No idempotence required.
func TestPromoteTagResumesAtTheWorkThatFailed(t *testing.T) {
	pub, grains, queue, _ := newPublisher(t)
	seedFolkTaggedWork(t, grains, "wtagged000001", "queer joy")
	seedFolkTaggedWork(t, grains, "wtagged000002", "queer joy")
	flaky := &failAfter{Store: grains, after: 1}
	pub.Blob = flaky

	promo, err := queue.ProposePromotion(t.Context(), "queer joy", vocab.TermRef{Scheme: "homosaurus", ID: transURI}, "mod")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pub.PromoteTag(t.Context(), promo, "lib@example.org"); err == nil {
		t.Fatal("the seeded failure did not fire")
	}

	flaky.off = true // the shard comes back
	works, err := pub.PromoteTag(t.Context(), promo, "lib@example.org")
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	// One work remained tagged, so the retry rewrites exactly it -- not both.
	if works != 1 {
		t.Fatalf("retry rewrote %d works, want 1 (the one that failed)", works)
	}
	for _, id := range []string{"wtagged000001", "wtagged000002"} {
		if tagged(t, grains, id) {
			t.Errorf("%s still carries the folk tag after the resume", id)
		}
		g, _, _ := grains.Get(t.Context(), bibframe.GrainPath(id))
		if !strings.Contains(string(g), transURI) {
			t.Errorf("%s never gained the authority subject", id)
		}
	}
}

// The skip only fires for editorial folk tags. A feed-side tag is deliberately
// never retracted -- the projector's alias suppression hides it -- so such a work
// matches the loop on every pass and a retry rewrites it again, idempotently.
// The rewrite is safe; the *count* is what this pins down, because the handler
// accumulates it across attempts.
func TestRetryRewritesFeedTaggedWorksAgain(t *testing.T) {
	pub, grains, queue, _ := newPublisher(t)
	seedTaggedWork(t, grains, "wtagged000001", "queer joy", true) // feed tag AND editorial
	flaky := &failAfter{Store: grains, after: 0}
	pub.Blob = flaky

	promo, err := queue.ProposePromotion(t.Context(), "queer joy", vocab.TermRef{Scheme: "homosaurus", ID: transURI}, "mod")
	if err != nil {
		t.Fatal(err)
	}
	if works, err := pub.PromoteTag(t.Context(), promo, "lib"); err == nil || works != 0 {
		t.Fatalf("seeded failure: works=%d err=%v", works, err)
	}
	flaky.off = true
	first, err := pub.PromoteTag(t.Context(), promo, "lib")
	if err != nil || first != 1 {
		t.Fatalf("first success: works=%d err=%v", first, err)
	}
	// The editorial tag is gone, but the feed tag remains, so it matches again.
	if tagged(t, grains, "wtagged000001") {
		t.Fatal("editorial tag survived the promotion")
	}
	again, err := pub.PromoteTag(t.Context(), promo, "lib")
	if err != nil {
		t.Fatal(err)
	}
	if again != 1 {
		t.Fatalf("a feed-tagged work was skipped on re-run (works=%d); if PromoteTag now retracts "+
			"feed tags, Promotion.Works's doc comment needs updating", again)
	}
}
