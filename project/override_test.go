package project

import (
	"testing"

	"github.com/freeeve/libcodex/rdf"

	"github.com/freeeve/libcat/bibframe"
)

// overrideCorpus builds a one-Work corpus: a feed subject + feed tag, then
// (optionally) an editorial override replacing the subject set.
func overrideCorpus(t *testing.T, withOverride bool) []byte {
	t.Helper()
	const bfNS = "http://id.loc.gov/ontologies/bibframe/"
	ds := &rdf.Dataset{}
	feed := bibframe.FeedGraph("overdrive")
	work := rdf.NewIRI(bibframe.WorkIRI("w1"))
	ds.Add(work, rdf.NewIRI("http://www.w3.org/1999/02/22-rdf-syntax-ns#type"), rdf.NewIRI(bfNS+"Work"), feed)
	title := rdf.NewBlank("t0")
	ds.Add(work, rdf.NewIRI(bfNS+"title"), title, feed)
	ds.Add(title, rdf.NewIRI(bfNS+"mainTitle"), rdf.NewLiteral("A Book", "", ""), feed)
	// Feed asserts one controlled subject and one blank-node tag.
	ds.Add(work, rdf.NewIRI(bfNS+"subject"), rdf.NewIRI("https://feed.example/subj-old"), feed)
	tag := rdf.NewBlank("s0")
	ds.Add(work, rdf.NewIRI(bfNS+"subject"), tag, feed)
	ds.Add(tag, rdf.NewIRI("http://www.w3.org/2000/01/rdf-schema#label"), rdf.NewLiteral("Feed Genre", "", ""), feed)
	nq, err := ds.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	if !withOverride {
		return nq
	}
	// Editorial claims bf:subject: replaces the controlled subject, keeps
	// the tag deliberately (partial removal = re-assert the keepers).
	patch := bibframe.OverridePatch(bibframe.WorkIRI("w1"), bfNS+"subject")
	patch.Add = append(patch.Add, bibframe.SubjectQuad("w1", "https://homosaurus.org/v4/better"))
	patch.Add = append(patch.Add, bibframe.TagQuad("w1", "Feed Genre"))
	nq, err = bibframe.ApplyEditorialPatch(nq, patch)
	if err != nil {
		t.Fatal(err)
	}
	return nq
}

func TestOverrideShadowsProjection(t *testing.T) {
	// Baseline: feed subject and tag project.
	cat, err := Project(overrideCorpus(t, false), "overdrive")
	if err != nil {
		t.Fatal(err)
	}
	w := cat.Works[0]
	if len(w.Subjects) != 1 || w.Subjects[0].ID != "https://feed.example/subj-old" {
		t.Fatalf("baseline subjects = %+v", w.Subjects)
	}
	if len(w.Tags) != 1 || w.Tags[0] != "Feed Genre" {
		t.Fatalf("baseline tags = %+v", w.Tags)
	}

	// Overridden: the editorial subject wins, the feed subject is shadowed,
	// the editorially re-asserted tag survives as lcat:tag, and unclaimed
	// properties (title) are untouched.
	cat, err = Project(overrideCorpus(t, true), "overdrive")
	if err != nil {
		t.Fatal(err)
	}
	w = cat.Works[0]
	if len(w.Subjects) != 1 || w.Subjects[0].ID != "https://homosaurus.org/v4/better" {
		t.Fatalf("overridden subjects = %+v", w.Subjects)
	}
	if len(w.Tags) != 1 || w.Tags[0] != "Feed Genre" {
		t.Fatalf("overridden tags = %+v", w.Tags)
	}
	if w.Title != "A Book" {
		t.Fatalf("title disturbed: %q", w.Title)
	}
}

// TestOverrideSurvivesReingest simulates the feed rewrite: the editorial
// graph (override + replacements) carries across, and the refreshed feed's
// re-asserted old subject stays shadowed.
func TestOverrideSurvivesReingest(t *testing.T) {
	const bfNS = "http://id.loc.gov/ontologies/bibframe/"
	grain := overrideCorpus(t, true)
	// Rewrite the feed graph from scratch (same statements, fresh pass) --
	// preservedQuads keeps every non-feed graph, as reingest does.
	ds, err := rdf.ParseNQuads(grain)
	if err != nil {
		t.Fatal(err)
	}
	feed := bibframe.FeedGraph("overdrive")
	keep := ds.Quads[:0]
	for _, q := range ds.Quads {
		if q.G != feed {
			keep = append(keep, q)
		}
	}
	ds.Quads = keep
	work := rdf.NewIRI(bibframe.WorkIRI("w1"))
	ds.Add(work, rdf.NewIRI("http://www.w3.org/1999/02/22-rdf-syntax-ns#type"), rdf.NewIRI(bfNS+"Work"), feed)
	title := rdf.NewBlank("fresh0")
	ds.Add(work, rdf.NewIRI(bfNS+"title"), title, feed)
	ds.Add(title, rdf.NewIRI(bfNS+"mainTitle"), rdf.NewLiteral("A Book, Refreshed", "", ""), feed)
	ds.Add(work, rdf.NewIRI(bfNS+"subject"), rdf.NewIRI("https://feed.example/subj-old"), feed)
	refreshed, err := ds.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	cat, err := Project(refreshed, "overdrive")
	if err != nil {
		t.Fatal(err)
	}
	w := cat.Works[0]
	if len(w.Subjects) != 1 || w.Subjects[0].ID != "https://homosaurus.org/v4/better" {
		t.Fatalf("override lost across reingest: %+v", w.Subjects)
	}
	if w.Title != "A Book, Refreshed" {
		t.Fatalf("feed refresh lost: %q", w.Title)
	}
}
