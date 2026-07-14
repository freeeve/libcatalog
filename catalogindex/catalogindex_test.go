package catalogindex

import (
	"reflect"
	"testing"
)

// sampleNQ is a miniature catalog.nq in the exact shape `lcat serialize`
// emits: bf:Work resources typed so gochickpeas labels them "Work", bf:subject
// links to authority IRIs (which carry their own IRI as the node "uri"), and a
// bare-label blank-node topic with no authority. w3 asserts the same controlled
// subject in two provenance graphs to exercise per-work dedup.
const sampleNQ = `<#w1Work> <http://www.w3.org/1999/02/22-rdf-syntax-ns#type> <http://id.loc.gov/ontologies/bibframe/Work> <feed:test> .
<#w2Work> <http://www.w3.org/1999/02/22-rdf-syntax-ns#type> <http://id.loc.gov/ontologies/bibframe/Work> <feed:test> .
<#w3Work> <http://www.w3.org/1999/02/22-rdf-syntax-ns#type> <http://id.loc.gov/ontologies/bibframe/Work> <feed:test> .
<#w1Work> <http://id.loc.gov/ontologies/bibframe/subject> <http://id.loc.gov/authorities/subjects/sh85021262> <editorial:> .
<#w1Work> <http://id.loc.gov/ontologies/bibframe/subject> <https://homosaurus.org/v5/hmit001> <feed:test> .
<#w2Work> <http://id.loc.gov/ontologies/bibframe/subject> <http://id.loc.gov/authorities/subjects/sh85021262> <feed:test> .
<#w2Work> <http://id.loc.gov/ontologies/bibframe/subject> <http://id.worldcat.org/fast/fst123> <feed:test> .
<#w3Work> <http://id.loc.gov/ontologies/bibframe/subject> <http://id.loc.gov/authorities/subjects/sh85021262> <feed:test> .
<#w3Work> <http://id.loc.gov/ontologies/bibframe/subject> <http://id.loc.gov/authorities/subjects/sh85021262> <editorial:> .
<#w3Work> <http://id.loc.gov/ontologies/bibframe/subject> _:cozy <editorial:> .
<#w3Work> <http://id.loc.gov/ontologies/bibframe/subject> <#w3local1> <feed:test> .
<http://id.loc.gov/authorities/subjects/sh85021262> <http://www.w3.org/2004/02/skos/core#prefLabel> "Cats"@en <authority:lcsh> .
<https://homosaurus.org/v5/hmit001> <http://www.w3.org/2004/02/skos/core#prefLabel> "Queer people"@en <authority:homosaurus> .
<http://id.worldcat.org/fast/fst123> <http://www.w3.org/2000/01/rdf-schema#label> "Widgets"@en <authority:fast> .
_:cozy <http://www.w3.org/2004/02/skos/core#prefLabel> "Cozy"@en <editorial:> .
<#w3local1> <http://www.w3.org/2004/02/skos/core#prefLabel> "Local heading"@en <feed:test> .
`

func mustSnapshot(t *testing.T) *Snapshot {
	t.Helper()
	s, err := FromNQuads([]byte(sampleNQ))
	if err != nil {
		t.Fatalf("FromNQuads: %v", err)
	}
	return s
}

// TestAuthorityUsageCountsAndOrder pins the whole-corpus usage tally: the shared
// LCSH heading counts three works (once for w3 despite its duplicate edge), the
// two single-use authorities carry their scheme and prefLabel/rdfs:label, and
// the result is sorted by descending use then URI.
func TestAuthorityUsageCountsAndOrder(t *testing.T) {
	got := mustSnapshot(t).AuthorityUsage()
	want := []AuthorityUse{
		{URI: "http://id.loc.gov/authorities/subjects/sh85021262", Label: "Cats", Scheme: "lcsh", Works: 3},
		{URI: "http://id.worldcat.org/fast/fst123", Label: "Widgets", Scheme: "fast", Works: 1},
		{URI: "https://homosaurus.org/v5/hmit001", Label: "Queer people", Scheme: "homosaurus", Works: 1},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("AuthorityUsage()\n got %+v\nwant %+v", got, want)
	}
}

// TestAuthorityUsageOmitsUncontrolled confirms neither the bare-label blank-node
// topic ("Cozy") nor the per-grain local fragment subject ("#w3local1", scheme
// unrecognized) leaks into the tally -- only recognized-scheme authorities do.
func TestAuthorityUsageOmitsUncontrolled(t *testing.T) {
	for _, u := range mustSnapshot(t).AuthorityUsage() {
		if u.Scheme == "" || u.Label == "Cozy" || u.Label == "Local heading" {
			t.Fatalf("uncontrolled subject leaked into usage: %+v", u)
		}
	}
}

// TestWorksUsingAuthority pins the reverse query: the works subject-linked to a
// heading, deduped and sorted by grain id, with w3 appearing once despite its
// duplicate provenance edges.
func TestWorksUsingAuthority(t *testing.T) {
	s := mustSnapshot(t)
	got := s.WorksUsingAuthority("http://id.loc.gov/authorities/subjects/sh85021262")
	want := []string{"w1", "w2", "w3"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("WorksUsingAuthority(Cats) = %v, want %v", got, want)
	}
	if only := s.WorksUsingAuthority("http://id.worldcat.org/fast/fst123"); !reflect.DeepEqual(only, []string{"w2"}) {
		t.Fatalf("WorksUsingAuthority(fast) = %v, want [w2]", only)
	}
}

// TestWorksUsingAuthorityUnknown returns nil for a URI no work references.
func TestWorksUsingAuthorityUnknown(t *testing.T) {
	if got := mustSnapshot(t).WorksUsingAuthority("http://id.loc.gov/authorities/subjects/sh00000000"); got != nil {
		t.Fatalf("unknown authority = %v, want nil", got)
	}
}
