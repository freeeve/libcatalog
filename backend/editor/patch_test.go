package editor

import (
	"strings"
	"testing"

	"github.com/freeeve/libcodex/rdf"

	"github.com/freeeve/libcat/bibframe"
)

func testGrain(t *testing.T) []byte {
	t.Helper()
	ds := &rdf.Dataset{}
	ds.Add(rdf.NewIRI(bibframe.WorkIRI("w1")),
		rdf.NewIRI("http://id.loc.gov/ontologies/bibframe/title"),
		rdf.NewLiteral("A Book", "", ""), bibframe.FeedGraph("overdrive"))
	nq, err := ds.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	return nq
}

func TestValidate(t *testing.T) {
	good := Patch{Add: []Statement{{
		S: bibframe.WorkIRI("w1"),
		P: "http://id.loc.gov/ontologies/bibframe/subject",
		O: Term{Kind: "iri", Value: "https://x"},
	}}}
	if err := good.Validate(nil); err != nil {
		t.Fatalf("good patch: %v", err)
	}
	cases := map[string]Patch{
		"empty": {},
		"empty term": {Add: []Statement{{S: "", P: "http://id.loc.gov/ontologies/bibframe/subject",
			O: Term{Kind: "iri", Value: "x"}}}},
		"bad kind": {Add: []Statement{{S: "#w1Work", P: "http://id.loc.gov/ontologies/bibframe/subject",
			O: Term{Kind: "blank", Value: "b0"}}}},
		"rogue predicate": {Add: []Statement{{S: "#w1Work", P: "http://evil.example/x",
			O: Term{Kind: "literal", Value: "x"}}}},
		"rogue removal": {Remove: []Statement{{S: "#w1Work", P: "http://evil.example/x",
			O: Term{Kind: "literal", Value: "x"}}}},
	}
	for name, p := range cases {
		if err := p.Validate(nil); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// Custom allowlist overrides the default.
	custom := Patch{Add: []Statement{{S: "#w1Work", P: "http://custom.example/pred",
		O: Term{Kind: "literal", Value: "x"}}}}
	if err := custom.Validate([]string{"http://custom.example/"}); err != nil {
		t.Fatalf("custom allowlist: %v", err)
	}
	if err := good.Validate([]string{"http://custom.example/"}); err == nil {
		t.Fatal("custom allowlist let bf: through")
	}
	// Oversized patch rejected.
	big := Patch{}
	for range 501 {
		big.Add = append(big.Add, good.Add[0])
	}
	if err := big.Validate(nil); err == nil {
		t.Fatal("oversized patch accepted")
	}
}

func TestComputeDiff(t *testing.T) {
	grain := testGrain(t)
	patch := Patch{Add: []Statement{
		{S: bibframe.WorkIRI("w1"), P: "http://id.loc.gov/ontologies/bibframe/subject",
			O: Term{Kind: "iri", Value: "https://homosaurus.org/v4/x"}},
		{S: bibframe.WorkIRI("w1"), P: bibframe.PredTag,
			O: Term{Kind: "literal", Value: "cozy fantasy", Lang: ""}},
	}}
	diff, updated, err := ComputeDiff(grain, patch)
	if err != nil {
		t.Fatal(err)
	}
	if len(diff.Added) != 2 || len(diff.Removed) != 0 {
		t.Fatalf("diff = %+v", diff)
	}
	// Round-trip: removing what was added restores the diff's inverse.
	inverse := Patch{Remove: patch.Add}
	diff2, restored, err := ComputeDiff(updated, inverse)
	if err != nil {
		t.Fatal(err)
	}
	if len(diff2.Removed) != 2 || len(diff2.Added) != 0 {
		t.Fatalf("inverse diff = %+v", diff2)
	}
	if string(restored) != string(grain) {
		t.Fatal("inverse did not restore the grain")
	}
	// Literal objects with language tags survive the wire conversion.
	langPatch := Patch{Add: []Statement{{
		S: "https://homosaurus.org/v4/x", P: "http://www.w3.org/2004/02/skos/core#prefLabel",
		O: Term{Kind: "literal", Value: "Etiqueta", Lang: "es"},
	}}}
	diff3, _, err := ComputeDiff(grain, langPatch)
	if err != nil || len(diff3.Added) != 1 || !strings.Contains(diff3.Added[0], `"Etiqueta"@es`) {
		t.Fatalf("lang literal diff = %+v (%v)", diff3, err)
	}
}
