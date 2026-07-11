package vocabsrc

import (
	"errors"
	"testing"

	"github.com/freeeve/libcat/ingest"
	"github.com/freeeve/libcat/storage/blob"

	"github.com/freeeve/libcat/backend/vocab"
)

// Two schemes side by side: a homosaurus term closeMatch-linked to an LCSH
// heading, plus an exactMatch pair, plus a retired LCSH target.
const crosswalkNT = `<https://homosaurus.org/v4/homoit1> <http://www.w3.org/2004/02/skos/core#prefLabel> "Gay men"@en <authority:homosaurus> .
<https://homosaurus.org/v4/homoit1> <http://www.w3.org/2004/02/skos/core#closeMatch> <http://id.loc.gov/authorities/subjects/sh1> <authority:homosaurus> .
<https://homosaurus.org/v4/homoit2> <http://www.w3.org/2004/02/skos/core#prefLabel> "Zines"@en <authority:homosaurus> .
<https://homosaurus.org/v4/homoit2> <http://www.w3.org/2004/02/skos/core#exactMatch> <http://id.loc.gov/authorities/subjects/sh2> <authority:homosaurus> .
<https://homosaurus.org/v4/homoit3> <http://www.w3.org/2004/02/skos/core#prefLabel> "Retired link"@en <authority:homosaurus> .
<https://homosaurus.org/v4/homoit3> <http://www.w3.org/2004/02/skos/core#closeMatch> <http://id.loc.gov/authorities/subjects/sh3> <authority:homosaurus> .
<http://id.loc.gov/authorities/subjects/sh1> <http://www.w3.org/2004/02/skos/core#prefLabel> "Gay men"@en <authority:lcsh> .
<http://id.loc.gov/authorities/subjects/sh1> <http://www.w3.org/2004/02/skos/core#broader> <http://id.loc.gov/authorities/subjects/shParent> <authority:lcsh> .
<http://id.loc.gov/authorities/subjects/shParent> <http://www.w3.org/2004/02/skos/core#prefLabel> "Gay people"@en <authority:lcsh> .
<http://id.loc.gov/authorities/subjects/shParent> <http://www.w3.org/2004/02/skos/core#broader> <http://id.loc.gov/authorities/subjects/shGrand> <authority:lcsh> .
<http://id.loc.gov/authorities/subjects/shGrand> <http://www.w3.org/2004/02/skos/core#prefLabel> "Sexual minorities"@en <authority:lcsh> .
<http://id.loc.gov/authorities/subjects/sh2> <http://www.w3.org/2004/02/skos/core#prefLabel> "Zines"@en <authority:lcsh> .
<http://id.loc.gov/authorities/subjects/sh3> <http://www.w3.org/2004/02/skos/core#prefLabel> "Old heading"@en <authority:lcsh> .
<http://id.loc.gov/authorities/subjects/sh3> <https://github.com/freeeve/libcat/ns#mergedInto> <http://id.loc.gov/authorities/subjects/sh2> <authority:lcsh> .
`

func crosswalkIndex(t *testing.T) *vocab.Index {
	t.Helper()
	bs := blob.NewMem()
	if _, err := bs.Put(t.Context(), "data/authorities/x.nq", []byte(crosswalkNT), blob.PutOptions{}); err != nil {
		t.Fatal(err)
	}
	ix, err := vocab.Load(t.Context(), bs, "data/authorities/", nil)
	if err != nil {
		t.Fatal(err)
	}
	return ix
}

func TestCrosswalkEnricher(t *testing.T) {
	ix := crosswalkIndex(t)
	e := NewCrosswalk(ix, "lcsh")
	if e.Name() != "crosswalk-lcsh" {
		t.Fatalf("name = %s", e.Name())
	}
	works := []ingest.WorkSummary{
		// closeMatch walks with 0.85 confidence.
		{WorkID: "w1", Subjects: []string{"https://homosaurus.org/v4/homoit1"}},
		// exactMatch walks with 1.0; the work already carrying the target
		// gains nothing.
		{WorkID: "w2", Subjects: []string{"https://homosaurus.org/v4/homoit2", "http://id.loc.gov/authorities/subjects/sh2"}},
		// A retired target is never suggested.
		{WorkID: "w3", Subjects: []string{"https://homosaurus.org/v4/homoit3"}},
		// LCSH subjects do not crosswalk into themselves.
		{WorkID: "w4", Subjects: []string{"http://id.loc.gov/authorities/subjects/sh1"}},
	}
	out, err := e.Enrich(t.Context(), works)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 {
		t.Fatalf("enrichments = %+v", out)
	}
	got := out[0]
	if got.WorkID != "w1" || len(got.Subjects) != 1 || got.Confidence != confCloseMatch {
		t.Fatalf("w1 enrichment = %+v", got)
	}
	if got.Subjects[0].URI != "http://id.loc.gov/authorities/subjects/sh1" || got.Subjects[0].Labels["en"] != "Gay men" {
		t.Fatalf("w1 subject = %+v", got.Subjects[0])
	}
	// The candidate's transitive broader chain rides along as standalone
	// term metadata, nearer ancestor first.
	if len(got.Terms) != 2 ||
		got.Terms[0].URI != "http://id.loc.gov/authorities/subjects/shParent" || got.Terms[0].Labels["en"] != "Gay people" ||
		got.Terms[1].URI != "http://id.loc.gov/authorities/subjects/shGrand" || got.Terms[1].Labels["en"] != "Sexual minorities" {
		t.Fatalf("w1 ancestor terms = %+v", got.Terms)
	}
	if len(got.Terms[0].Broader) != 1 || got.Terms[0].Broader[0] != "http://id.loc.gov/authorities/subjects/shGrand" {
		t.Fatalf("parent term broader = %+v", got.Terms[0].Broader)
	}
}

func TestCacheTermResolvesForever(t *testing.T) {
	s := newService(t)
	ctx := t.Context()
	ix, err := vocab.Load(ctx, s.Blob, s.AuthoritiesPrefix, nil)
	if err != nil {
		t.Fatal(err)
	}
	s.Index = ix

	sugg := Suggestion{
		Source: "wikidata", Scheme: "wikidata",
		ID: "http://www.wikidata.org/entity/Q69990794", Label: "non-binary gender",
		Description: "gender identity outside the binary",
		ExactMatch:  []string{"https://homosaurus.org/v4/homoit0000505"},
	}
	if err := s.CacheTerm(ctx, sugg); err != nil {
		t.Fatal(err)
	}
	term, ok := ix.Resolve("http://www.wikidata.org/entity/Q69990794")
	if !ok || term.Scheme != "wikidata" || term.Label("en") != "non-binary gender" {
		t.Fatalf("cached term = %+v (ok=%v)", term, ok)
	}
	if len(term.ExactMatch) != 1 || term.Definition["en"] == "" {
		t.Fatalf("cached term details = %+v", term)
	}
	// Idempotent: re-caching is a no-op, not an error.
	if err := s.CacheTerm(ctx, sugg); err != nil {
		t.Fatal(err)
	}
	// A configured scheme filter picks the cached scheme up automatically.
	s.BaseSchemes = []string{"local"}
	schemes, err := s.Schemes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, scheme := range schemes {
		if scheme == "wikidata" {
			found = true
		}
	}
	if !found {
		t.Fatalf("schemes = %v, want wikidata included", schemes)
	}
	// Survives a full reload from the blob store.
	if err := s.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	if _, ok := ix.Resolve("http://www.wikidata.org/entity/Q69990794"); !ok {
		t.Fatal("cached term lost on reload")
	}
	// Validation floor.
	if err := s.CacheTerm(ctx, Suggestion{Scheme: "x", ID: "not-a-uri", Label: "y"}); !errors.Is(err, ErrValidation) {
		t.Fatalf("bad id err = %v", err)
	}
	if err := s.CacheTerm(ctx, Suggestion{Scheme: "", ID: "https://x", Label: "y"}); !errors.Is(err, ErrValidation) {
		t.Fatalf("no scheme err = %v", err)
	}
}
