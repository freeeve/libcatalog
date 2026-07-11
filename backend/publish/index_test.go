package publish

import (
	"context"
	"strings"
	"testing"

	"github.com/freeeve/libcat/bibframe"

	"github.com/freeeve/libcat/backend/suggest"
	"github.com/freeeve/libcat/backend/vocab"
)

// fakeIndex records the read-your-writes calls publish must make.
type fakeIndex struct {
	applied map[string]string // grain path -> etag
	grains  map[string]int    // grain path -> written bytes
	feeds   [][]string
}

func (f *fakeIndex) Apply(path, etag string, grain []byte) {
	if f.applied == nil {
		f.applied = map[string]string{}
		f.grains = map[string]int{}
	}
	f.applied[path] = etag
	f.grains[path] = len(grain)
}

func (f *fakeIndex) AppendFeed(_ context.Context, paths ...string) error {
	f.feeds = append(f.feeds, paths)
	return nil
}

// TestPromoteTagUpdatesIndex covers an approved promotion keeps
// the shared work index exact -- Apply per rewritten work grain, one
// AppendFeed over the work paths only (the alias grain feeds the rebuild
// trigger, never the index feed).
func TestPromoteTagUpdatesIndex(t *testing.T) {
	pub, grains, queue, _ := newPublisher(t)
	ix := &fakeIndex{}
	pub.Index = ix
	seedTaggedWork(t, grains, "wtagged000001", "queer joy", true)
	seedTaggedWork(t, grains, "wtagged000002", "queer joy", false)

	term := vocab.TermRef{Scheme: "homosaurus", ID: transURI}
	if _, err := queue.ProposePromotion(t.Context(), "queer joy", term, "mod@example.org"); err != nil {
		t.Fatal(err)
	}
	n, err := pub.PromoteTag(t.Context(), suggest.Promotion{Tag: "queer joy", Term: term}, "lib@example.org")
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("rewritten = %d", n)
	}
	for _, id := range []string{"wtagged000001", "wtagged000002"} {
		path := bibframe.GrainPath(id)
		if ix.applied[path] == "" || ix.grains[path] == 0 {
			t.Fatalf("%s not applied to the index: %+v", id, ix.applied)
		}
	}
	if _, ok := ix.applied[aliasGrainPath]; ok {
		t.Fatal("alias grain applied to the work index")
	}
	if len(ix.feeds) != 1 || len(ix.feeds[0]) != 2 {
		t.Fatalf("feed appends = %+v, want one call with the two work paths", ix.feeds)
	}
	for _, p := range ix.feeds[0] {
		if p == aliasGrainPath {
			t.Fatal("alias grain fed to the index feed")
		}
	}

	// The alias bookkeeping lands OUTSIDE the authority: namespace, so the
	// vocab loader can never mint an "aliases" scheme from it.
	alias, _, err := grains.Get(t.Context(), aliasGrainPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(alias), "<lcat:aliases>") || strings.Contains(string(alias), "<authority:aliases>") {
		t.Fatalf("alias grain graph wrong:\n%s", alias)
	}
}

// TestPublishApprovedUpdatesIndex covers the same contract on the approved-
// suggestions path, which shares the Publisher.
func TestPublishApprovedUpdatesIndex(t *testing.T) {
	pub, grains, queue, _ := newPublisher(t)
	ix := &fakeIndex{}
	pub.Index = ix
	path := seedGrain(t, grains)

	if err := queue.ManualTerm(t.Context(), workID, vocab.TermRef{Scheme: "homosaurus", ID: transURI}, "A Book", "lib"); err != nil {
		t.Fatal(err)
	}
	res, err := pub.PublishApproved(t.Context(), "lib@example.org")
	if err != nil {
		t.Fatal(err)
	}
	if res.Published == 0 {
		t.Fatalf("result = %+v", res)
	}
	if ix.applied[path] == "" || ix.grains[path] == 0 {
		t.Fatalf("published grain not applied: %+v", ix.applied)
	}
	if len(ix.feeds) != 1 || len(ix.feeds[0]) != 1 || ix.feeds[0][0] != path {
		t.Fatalf("feed appends = %+v", ix.feeds)
	}
}
