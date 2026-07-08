package workindex

import (
	"testing"

	"github.com/freeeve/libcat/bibframe"
	"github.com/freeeve/libcat/storage/blob"
)

// TestFeedReadYourWritesAcrossContainers: a write on one index instance becomes
// visible on another (that loaded the same snapshot) via one feed GET -- no
// grain re-read and no corpus List.
func TestFeedReadYourWritesAcrossContainers(t *testing.T) {
	ctx := t.Context()
	cs := &countingStore{Store: blob.NewMem()}
	seed(t, cs, "w1", grain("w1", "A Wizard of Earthsea", "Le Guin, Ursula K.", "9780547773742", ""))
	seed(t, cs, "w2", grain("w2", "The Tombs of Atuan", "Le Guin, Ursula K.", "9780689845369", ""))

	// Container A warms and snapshots.
	a := New(cs, "data/works/")
	if _, err := a.Summaries(ctx); err != nil {
		t.Fatal(err)
	}
	if err := a.Save(ctx); err != nil {
		t.Fatal(err)
	}

	// Container B loads the snapshot and reads once (priming its clocks).
	b := New(cs, "data/works/")
	if err := b.LoadSnapshot(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Summaries(ctx); err != nil {
		t.Fatal(err)
	}

	// A writes w3 and publishes it to the feed.
	updated := grain("w3", "The Farthest Shore", "Le Guin, Ursula K.", "9780689852305", "")
	etag := seed(t, cs, "w3", updated)
	a.Apply(bibframe.GrainPath("w3"), etag, updated)
	if err := a.AppendFeed(ctx, bibframe.GrainPath("w3")); err != nil {
		t.Fatal(err)
	}

	// Lapse only B's feed clock (not its List clock): the next read picks up w3
	// from the feed with a single GET and no grain reads or List.
	b.feedAt = b.feedAt.Add(-2 * DefaultFeedTTL)
	cs.gets.Store(0)
	cs.lists.Store(0)
	sums, err := b.Summaries(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(sums) != 3 || sums[2].WorkID != "w3" {
		t.Fatalf("B summaries after feed poll = %+v", sums)
	}
	if g, l := cs.gets.Load(), cs.lists.Load(); g != 1 || l != 0 {
		t.Fatalf("feed read = %d gets, %d lists; want 1 get (feed only), 0 lists", g, l)
	}
}

// TestFeedTombstonePropagates: a delete published to the feed removes the work
// on another container.
func TestFeedTombstonePropagates(t *testing.T) {
	ctx := t.Context()
	cs := &countingStore{Store: blob.NewMem()}
	seed(t, cs, "w1", grain("w1", "A Wizard of Earthsea", "Le Guin, Ursula K.", "9780547773742", ""))
	seed(t, cs, "w2", grain("w2", "The Tombs of Atuan", "Le Guin, Ursula K.", "9780689845369", ""))
	a := New(cs, "data/works/")
	if _, err := a.Summaries(ctx); err != nil {
		t.Fatal(err)
	}
	if err := a.Save(ctx); err != nil {
		t.Fatal(err)
	}
	b := New(cs, "data/works/")
	if err := b.LoadSnapshot(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Summaries(ctx); err != nil {
		t.Fatal(err)
	}

	// A deletes w2 (grain gone, Update drops it) and publishes the tombstone.
	if err := cs.Delete(ctx, bibframe.GrainPath("w2")); err != nil {
		t.Fatal(err)
	}
	if err := a.Update(ctx, bibframe.GrainPath("w2")); err != nil {
		t.Fatal(err)
	}
	if err := a.AppendFeed(ctx, bibframe.GrainPath("w2")); err != nil {
		t.Fatal(err)
	}

	b.feedAt = b.feedAt.Add(-2 * DefaultFeedTTL)
	sums, err := b.Summaries(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(sums) != 1 || sums[0].WorkID != "w1" {
		t.Fatalf("B summaries after tombstone = %+v", sums)
	}
}

// TestFeedFoldsPastThreshold: once the feed would exceed the fold threshold, an
// append folds into a fresh snapshot at the next epoch and empties the feed, so
// a reader loads the new base instead of replaying a large feed.
func TestFeedFoldsPastThreshold(t *testing.T) {
	ctx := t.Context()
	cs := &countingStore{Store: blob.NewMem()}
	seed(t, cs, "w1", grain("w1", "A Wizard of Earthsea", "Le Guin, Ursula K.", "9780547773742", ""))
	a := New(cs, "data/works/")
	a.foldThreshold = 2
	if _, err := a.Summaries(ctx); err != nil {
		t.Fatal(err)
	}
	if err := a.Save(ctx); err != nil {
		t.Fatal(err)
	}

	// Append three works: the third pushes past the threshold and triggers a fold.
	for _, id := range []string{"w2", "w3", "w4"} {
		g := grain(id, "T "+id, "Le Guin, Ursula K.", "978000000000"+id[1:], "")
		etag := seed(t, cs, id, g)
		a.Apply(bibframe.GrainPath(id), etag, g)
		if err := a.AppendFeed(ctx, bibframe.GrainPath(id)); err != nil {
			t.Fatal(err)
		}
	}

	// The feed is now empty at epoch 1; the snapshot carries all four works.
	file, _, exists, err := a.getFeedFile(ctx)
	if err != nil || !exists {
		t.Fatalf("feed after fold: exists=%v err=%v", exists, err)
	}
	if file.Epoch != 1 || len(file.Records) != 0 {
		t.Fatalf("feed after fold = epoch %d, %d records; want epoch 1, 0 records", file.Epoch, len(file.Records))
	}

	c := New(cs, "data/works/")
	if err := c.LoadSnapshot(ctx); err != nil {
		t.Fatal(err)
	}
	c.feedActive = false // read the folded base directly
	sums, err := c.Summaries(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(sums) != 4 {
		t.Fatalf("folded snapshot summaries = %d, want 4", len(sums))
	}
	if c.epoch != 1 {
		t.Fatalf("reader epoch = %d, want 1", c.epoch)
	}
}
