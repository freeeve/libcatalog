package bibframe_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/freeeve/libcodex/rdf"

	"github.com/freeeve/libcat/bibframe"
)

func TestAuthorityGrainPath(t *testing.T) {
	p := bibframe.AuthorityGrainPath("a0123456789abc")
	if !strings.HasPrefix(p, "data/authorities/") || !strings.HasSuffix(p, "/a0123456789abc.nq") {
		t.Fatalf("path = %q", p)
	}
	if p != bibframe.AuthorityGrainPath("a0123456789abc") {
		t.Fatal("path not deterministic")
	}
}

func TestAuthorityGrainRoundTrip(t *testing.T) {
	term := bibframe.AuthorityTerm{
		URI:        bibframe.LocalAuthorityIRI("atest00000001"),
		PrefLabel:  map[string]string{"en": "Necromancy in fiction", "es": "Nigromancia en la ficción"},
		AltLabel:   map[string][]string{"en": {"Necromancers (Fiction)"}},
		Definition: map[string]string{"en": "Works where the dead are raised."},
		Broader:    []string{"https://example.org/vocab/magic"},
		Narrower:   []string{"https://example.org/vocab/bone-magic"},
		Related:    []string{"https://example.org/vocab/ghosts"},
		ExactMatch: []string{"http://id.loc.gov/authorities/subjects/sh0000001"},
	}
	grain, err := bibframe.BuildAuthorityGrain(nil, term, "local")
	if err != nil {
		t.Fatal(err)
	}
	got, err := bibframe.ParseAuthorityGrain(grain, term.URI, "local")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, term) {
		t.Fatalf("round trip:\n got %+v\nwant %+v", got, term)
	}

	// A description rewrite replaces the graph wholesale and stays parseable.
	term.PrefLabel["en"] = "Necromancy"
	term.Related = nil
	grain2, err := bibframe.BuildAuthorityGrain(grain, term, "local")
	if err != nil {
		t.Fatal(err)
	}
	got2, err := bibframe.ParseAuthorityGrain(grain2, term.URI, "local")
	if err != nil {
		t.Fatal(err)
	}
	if got2.PrefLabel["en"] != "Necromancy" || got2.Related != nil {
		t.Fatalf("after rewrite: %+v", got2)
	}
}

func TestAuthorityMergeMarker(t *testing.T) {
	loser := bibframe.LocalAuthorityIRI("aloser0000001")
	winner := "https://homosaurus.org/v4/homoit0001235"
	grain, err := bibframe.BuildAuthorityGrain(nil, bibframe.AuthorityTerm{
		URI: loser, PrefLabel: map[string]string{"en": "Trans people"},
	}, "local")
	if err != nil {
		t.Fatal(err)
	}
	marked, err := bibframe.AddAuthorityMergeMarker(grain, loser, winner, "local")
	if err != nil {
		t.Fatal(err)
	}
	again, err := bibframe.AddAuthorityMergeMarker(marked, loser, winner, "local")
	if err != nil {
		t.Fatal(err)
	}
	if string(again) != string(marked) {
		t.Fatal("merge marker not idempotent")
	}
	term, err := bibframe.ParseAuthorityGrain(marked, loser, "local")
	if err != nil {
		t.Fatal(err)
	}
	if term.MergedInto != winner {
		t.Fatalf("mergedInto = %q", term.MergedInto)
	}
	// The retirement survives a description rebuild (Quads re-emits it).
	rebuilt, err := bibframe.BuildAuthorityGrain(marked, term, "local")
	if err != nil {
		t.Fatal(err)
	}
	term2, err := bibframe.ParseAuthorityGrain(rebuilt, loser, "local")
	if err != nil {
		t.Fatal(err)
	}
	if term2.MergedInto != winner {
		t.Fatalf("mergedInto lost on rebuild: %+v", term2)
	}
}

// mergeWorkFixture builds a Work grain carrying a feed title, an editorial
// bf:subject reference to the loser term, and the loser's authority-graph
// labels the projector resolves.
func mergeWorkFixture(t *testing.T, workID, loserURI string) []byte {
	t.Helper()
	ds := &rdf.Dataset{}
	feed := bibframe.FeedGraph("overdrive")
	work := rdf.NewIRI(bibframe.WorkIRI(workID))
	ds.Add(work, rdf.NewIRI("http://id.loc.gov/ontologies/bibframe/title"), rdf.NewLiteral("A Book", "", ""), feed)
	nq, err := ds.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	nq, err = bibframe.AppendAuthoritySubject(nq, workID, bibframe.AuthoritySubject{
		URI: loserURI, Labels: map[string]string{"en": "Old Heading"},
	}, "local")
	if err != nil {
		t.Fatal(err)
	}
	return nq
}

func TestReplaceSubjectReference(t *testing.T) {
	const workID = "wmerge0000001"
	loser := bibframe.LocalAuthorityIRI("aloser0000001")
	winner := bibframe.AuthoritySubject{
		URI:     "http://id.loc.gov/authorities/subjects/sh0000001",
		Labels:  map[string]string{"en": "New Heading"},
		Broader: []string{"http://id.loc.gov/authorities/subjects/sh0000000"},
	}
	grain := mergeWorkFixture(t, workID, loser)
	out, err := bibframe.ReplaceSubjectReference(grain, workID, loser, winner, "lcsh")
	if err != nil {
		t.Fatal(err)
	}
	text := string(out)
	if strings.Contains(text, loser) {
		t.Fatalf("loser reference survives:\n%s", text)
	}
	if !strings.Contains(text, winner.URI) || !strings.Contains(text, "New Heading") {
		t.Fatalf("winner not appended:\n%s", text)
	}
	if !strings.Contains(text, "A Book") {
		t.Fatalf("feed statements disturbed:\n%s", text)
	}
	// Idempotent on a grain that no longer references the loser.
	again, err := bibframe.ReplaceSubjectReference(out, workID, loser, winner, "lcsh")
	if err != nil {
		t.Fatal(err)
	}
	if string(again) != text {
		t.Fatal("rewrite not idempotent")
	}
}
