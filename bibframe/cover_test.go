package bibframe

import (
	"testing"

	"github.com/freeeve/libcodex/rdf"
)

func coverGrain(t *testing.T, workID string) []byte {
	t.Helper()
	ds := &rdf.Dataset{}
	ds.Add(rdf.NewIRI(WorkIRI(workID)),
		rdf.NewIRI("http://id.loc.gov/ontologies/bibframe/title"),
		rdf.NewLiteral("A Book", "", ""), FeedGraph("overdrive"))
	nq, err := ds.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	return nq
}

// The editor's Cover panel reads the cover by name, because it is not a profile
// field and so never appears in the doc's fields map (tasks/242).
func TestCoverOfReadsEditorialAndFeedCovers(t *testing.T) {
	const workID = "wcover0000001"
	grain := coverGrain(t, workID)

	got, err := CoverOf(grain, workID)
	if err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Fatalf("a work with no cover reads %q", got)
	}

	withCover, err := SetCover(grain, workID, "covers/"+workID+".png")
	if err != nil {
		t.Fatal(err)
	}
	if got, err = CoverOf(withCover, workID); err != nil || got != "covers/"+workID+".png" {
		t.Fatalf("CoverOf = (%q, %v)", got, err)
	}

	// SetCover("") drops the editorial layer, so a feed-carried cover shows
	// through again -- which is the documented contract, and the reason CoverOf
	// cannot simply read the editorial graph.
	feed, err := ApplyPatch(grain, FeedGraph("overdrive"), Patch{Add: []rdf.Quad{{
		S: rdf.NewIRI(WorkIRI(workID)),
		P: rdf.NewIRI(ExtraPred + CoverExtraKey),
		O: rdf.NewLiteral("https://provider.example/art.jpg", "", ""),
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if got, err = CoverOf(feed, workID); err != nil || got != "https://provider.example/art.jpg" {
		t.Fatalf("feed cover = (%q, %v)", got, err)
	}
	// An editorial cover overlays the feed one.
	both, err := SetCover(feed, workID, "covers/"+workID+".webp")
	if err != nil {
		t.Fatal(err)
	}
	if got, err = CoverOf(both, workID); err != nil || got != "covers/"+workID+".webp" {
		t.Fatalf("editorial should win: (%q, %v)", got, err)
	}
	// Removing the editorial one reveals the feed one.
	cleared, err := SetCover(both, workID, "")
	if err != nil {
		t.Fatal(err)
	}
	if got, err = CoverOf(cleared, workID); err != nil || got != "https://provider.example/art.jpg" {
		t.Fatalf("feed cover did not show through: (%q, %v)", got, err)
	}
}

// A cover belongs to the work it is stated on; another work's grain-mate must
// not leak into it.
func TestCoverOfIsScopedToItsWork(t *testing.T) {
	const workID, other = "wcover0000001", "wcover0000002"
	grain := coverGrain(t, workID)
	grain, err := ApplyEditorialPatch(grain, Patch{Add: []rdf.Quad{{
		S: rdf.NewIRI(WorkIRI(other)),
		P: rdf.NewIRI(ExtraPred + CoverExtraKey),
		O: rdf.NewLiteral("covers/"+other+".png", "", ""),
	}}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := CoverOf(grain, workID)
	if err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Fatalf("%s picked up %s's cover: %q", workID, other, got)
	}
}
