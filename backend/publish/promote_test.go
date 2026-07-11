package publish

import (
	"strings"
	"testing"

	"github.com/freeeve/libcodex/rdf"

	"github.com/freeeve/libcat/bibframe"
	"github.com/freeeve/libcat/project"
	"github.com/freeeve/libcat/storage/blob"

	"github.com/freeeve/libcat/backend/suggest"
	"github.com/freeeve/libcat/backend/vocab"
)

// seedTaggedWork writes a grain whose Work carries a feed blank-node tag
// and, optionally, the same tag editorially (the folk shape).
func seedTaggedWork(t *testing.T, bs blob.Store, workID, tag string, editorialToo bool) {
	t.Helper()
	const bfNS = "http://id.loc.gov/ontologies/bibframe/"
	ds := &rdf.Dataset{}
	feed := bibframe.FeedGraph("overdrive")
	work := rdf.NewIRI(bibframe.WorkIRI(workID))
	ds.Add(work, rdf.NewIRI("http://www.w3.org/1999/02/22-rdf-syntax-ns#type"), rdf.NewIRI(bfNS+"Work"), feed)
	title := rdf.NewBlank("t0")
	ds.Add(work, rdf.NewIRI(bfNS+"title"), title, feed)
	ds.Add(title, rdf.NewIRI(bfNS+"mainTitle"), rdf.NewLiteral("Book "+workID, "", ""), feed)
	tagNode := rdf.NewBlank("s0")
	ds.Add(work, rdf.NewIRI(bfNS+"subject"), tagNode, feed)
	ds.Add(tagNode, rdf.NewIRI("http://www.w3.org/2000/01/rdf-schema#label"), rdf.NewLiteral(tag, "", ""), feed)
	nq, err := ds.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	if editorialToo {
		nq, err = bibframe.ApplyEditorialPatch(nq, bibframe.Patch{Add: []rdf.Quad{
			bibframe.TagQuad(workID, tag),
		}})
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := bs.Put(t.Context(), bibframe.GrainPath(workID), nq, blob.PutOptions{}); err != nil {
		t.Fatal(err)
	}
}

func TestPromoteTagEndToEnd(t *testing.T) {
	pub, grains, queue, notifier := newPublisher(t)
	// Two works carry the tag ("science fiction" is lowercase in summaries
	// via the editorial folk shape; feed blank labels pass through as-is,
	// so use a lowercase tag for a clean match).
	seedTaggedWork(t, grains, "wtagged000001", "queer joy", true)
	seedTaggedWork(t, grains, "wtagged000002", "queer joy", false)
	seedTaggedWork(t, grains, "wother0000003", "something else", false)

	// Propose (moderator) and approve (librarian).
	term := vocab.TermRef{Scheme: "homosaurus", ID: transURI}
	promo, err := queue.ProposePromotion(t.Context(), "Queer  JOY", term, "mod@example.org")
	if err != nil {
		t.Fatalf("propose: %v", err)
	}
	if promo.Tag != "queer joy" || promo.Term.Label != "Transgender people" {
		t.Fatalf("promo = %+v", promo)
	}
	// Duplicate proposal conflicts while pending.
	if _, err := queue.ProposePromotion(t.Context(), "queer joy", term, "x"); err != suggest.ErrPromotionExists {
		t.Fatalf("duplicate: %v", err)
	}
	// Execute, then stamp. PromoteTag reads only the tag and the term,
	// so the pending promotion is all it needs, and the APPROVED record is never
	// durable ahead of the rewrite it describes.
	works, err := pub.PromoteTag(t.Context(), promo, "lib@example.org")
	if err != nil {
		t.Fatalf("PromoteTag: %v", err)
	}
	if works != 2 {
		t.Fatalf("rewrote %d works, want 2", works)
	}
	decided, err := queue.ApprovePromotion(t.Context(), "queer joy", "lib@example.org", works)
	if err != nil || decided.Status != suggest.StatusApproved {
		t.Fatalf("approve: %+v, %v", decided, err)
	}
	if decided.Works != 2 {
		t.Fatalf("approved record says %d works, want 2", decided.Works)
	}

	// Both carriers gained the subject; the editorial tag was retracted.
	g1, _, _ := grains.Get(t.Context(), bibframe.GrainPath("wtagged000001"))
	if !strings.Contains(string(g1), transURI) {
		t.Fatalf("subject missing:\n%s", g1)
	}
	if strings.Contains(string(g1), bibframe.PredTag) {
		t.Fatalf("editorial tag not retracted:\n%s", g1)
	}
	// The untagged work is untouched.
	g3, _, _ := grains.Get(t.Context(), bibframe.GrainPath("wother0000003"))
	if strings.Contains(string(g3), transURI) {
		t.Fatal("unrelated work rewritten")
	}
	// The alias grain records the subsumption.
	aliases, _, err := grains.Get(t.Context(), aliasGrainPath)
	if err != nil || !strings.Contains(string(aliases), bibframe.PredTagAlias) || !strings.Contains(string(aliases), "queer joy") {
		t.Fatalf("alias grain = %s (%v)", aliases, err)
	}
	// Trigger fired with the changed paths (2 works + alias grain).
	last := notifier.events[len(notifier.events)-1]
	if len(last.Paths) != 3 {
		t.Fatalf("trigger paths = %v", last.Paths)
	}

	// Projection: works carrying the term suppress the residual feed tag;
	// serialize the tree and project.
	var corpus []byte
	var enc rdf.Encoder // one encoder across grains so blank labels stay distinct
	for entry, err := range grains.List(t.Context(), "data/") {
		if err != nil {
			t.Fatal(err)
		}
		data, _, _ := grains.Get(t.Context(), entry.Path)
		ds, err := rdf.ParseNQuads(data)
		if err != nil {
			t.Fatal(err)
		}
		for _, gt := range ds.Graphs() {
			corpus = append(corpus, enc.AppendNQuads(nil, ds.Graph(gt), gt)...)
		}
	}
	cat, err := project.Project(corpus, "overdrive")
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range cat.Works {
		switch w.ID {
		case "wtagged000001", "wtagged000002":
			if len(w.Subjects) != 1 || w.Subjects[0].ID != transURI {
				t.Fatalf("%s subjects = %+v", w.ID, w.Subjects)
			}
			for _, tag := range w.Tags {
				if tag == "queer joy" {
					t.Fatalf("%s still shows the promoted tag", w.ID)
				}
			}
		case "wother0000003":
			if len(w.Tags) != 1 || w.Tags[0] != "something else" {
				t.Fatalf("unrelated tags = %v", w.Tags)
			}
		}
	}

	// Re-promotion of a decided tag conflicts; re-execution is idempotent.
	if _, err := queue.ApprovePromotion(t.Context(), "queer joy", "lib", 0); err == nil {
		t.Fatal("re-decide accepted")
	}
	worksAgain, err := pub.PromoteTag(t.Context(), decided, "lib@example.org")
	if err != nil {
		t.Fatal(err)
	}
	// The editorial tags are gone, so only the feed-tag carrier matches now;
	// AppendAuthoritySubject is idempotent so grains do not change shape.
	_ = worksAgain
	g1again, _, _ := grains.Get(t.Context(), bibframe.GrainPath("wtagged000001"))
	if !strings.Contains(string(g1again), transURI) {
		t.Fatal("idempotent re-run lost the subject")
	}
}
