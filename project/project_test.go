package project

import (
	"reflect"
	"testing"
)

// A one-Work catalog: feed data plus one editorial (curated) subject IRI.
const sampleCatalog = `<#w1Work> <http://www.w3.org/1999/02/22-rdf-syntax-ns#type> <http://id.loc.gov/ontologies/bibframe/Work> <feed:overdrive> .
<#w1Work> <http://www.w3.org/1999/02/22-rdf-syntax-ns#type> <http://id.loc.gov/ontologies/bibframe/Text> <feed:overdrive> .
<#w1Work> <http://id.loc.gov/ontologies/bibframe/title> _:t <feed:overdrive> .
_:t <http://id.loc.gov/ontologies/bibframe/mainTitle> "Herculine" <feed:overdrive> .
_:t <http://id.loc.gov/ontologies/bibframe/subtitle> "A Novel" <feed:overdrive> .
<#w1Work> <http://id.loc.gov/ontologies/bibframe/contribution> _:c1 <feed:overdrive> .
_:c1 <http://www.w3.org/1999/02/22-rdf-syntax-ns#type> <http://id.loc.gov/ontologies/bflc/PrimaryContribution> <feed:overdrive> .
_:c1 <http://id.loc.gov/ontologies/bibframe/agent> _:a1 <feed:overdrive> .
_:a1 <http://www.w3.org/2000/01/rdf-schema#label> "Byron, Grace" <feed:overdrive> .
_:c1 <http://id.loc.gov/ontologies/bibframe/role> _:r1 <feed:overdrive> .
_:r1 <http://www.w3.org/2000/01/rdf-schema#label> "author" <feed:overdrive> .
<#w1Work> <http://id.loc.gov/ontologies/bibframe/contribution> _:c2 <feed:overdrive> .
_:c2 <http://id.loc.gov/ontologies/bibframe/agent> _:a2 <feed:overdrive> .
_:a2 <http://www.w3.org/2000/01/rdf-schema#label> "Endres, Nicky" <feed:overdrive> .
_:c2 <http://id.loc.gov/ontologies/bibframe/role> _:r2 <feed:overdrive> .
_:r2 <http://www.w3.org/2000/01/rdf-schema#label> "narrator" <feed:overdrive> .
<#w1Work> <http://id.loc.gov/ontologies/bibframe/subject> _:s1 <feed:overdrive> .
_:s1 <http://www.w3.org/2000/01/rdf-schema#label> "Fiction" <feed:overdrive> .
<#w1Work> <http://id.loc.gov/ontologies/bibframe/subject> <https://homosaurus.org/v3/homoit0000669> <editorial:> .
<#w1Work> <http://id.loc.gov/ontologies/bibframe/language> <http://id.loc.gov/vocabulary/languages/eng> <feed:overdrive> .
<#w1Work> <http://id.loc.gov/ontologies/bibframe/classification> _:cl <feed:overdrive> .
_:cl <http://id.loc.gov/ontologies/bibframe/classificationPortion> "FIC073000" <feed:overdrive> .
<#w1Work> <http://id.loc.gov/ontologies/bibframe/hasInstance> <#i1Instance> <feed:overdrive> .
<#i1Instance> <http://www.w3.org/1999/02/22-rdf-syntax-ns#type> <http://id.loc.gov/ontologies/bibframe/Instance> <feed:overdrive> .
<#i1Instance> <http://id.loc.gov/ontologies/bibframe/identifiedBy> _:id1 <feed:overdrive> .
_:id1 <http://www.w3.org/1999/02/22-rdf-syntax-ns#type> <http://id.loc.gov/ontologies/bibframe/Isbn> <feed:overdrive> .
_:id1 <http://www.w3.org/1999/02/22-rdf-syntax-ns#value> "9781668128251" <feed:overdrive> .
<#i1Instance> <http://id.loc.gov/ontologies/bibframe/identifiedBy> _:id2 <feed:overdrive> .
_:id2 <http://www.w3.org/1999/02/22-rdf-syntax-ns#type> <http://id.loc.gov/ontologies/bibframe/Identifier> <feed:overdrive> .
_:id2 <http://www.w3.org/1999/02/22-rdf-syntax-ns#value> "11682058" <feed:overdrive> .
_:id2 <http://id.loc.gov/ontologies/bibframe/source> _:src2 <feed:overdrive> .
_:src2 <http://www.w3.org/2000/01/rdf-schema#label> "overdrive" <feed:overdrive> .
`

func TestProject(t *testing.T) {
	cat, err := Project([]byte(sampleCatalog), "overdrive")
	if err != nil {
		t.Fatalf("Project: %v", err)
	}
	if cat.Version != SchemaVersion {
		t.Errorf("version = %d, want %d", cat.Version, SchemaVersion)
	}
	if len(cat.Works) != 1 {
		t.Fatalf("got %d works, want 1", len(cat.Works))
	}
	w := cat.Works[0]

	if w.ID != "w1" || w.Title != "Herculine" || w.Subtitle != "A Novel" {
		t.Errorf("work header = %+v", w)
	}
	wantContribs := []Contributor{
		{Name: "Byron, Grace", Role: "author"}, // primary leads
		{Name: "Endres, Nicky", Role: "narrator"},
	}
	if !reflect.DeepEqual(w.Contributors, wantContribs) {
		t.Errorf("contributors = %+v, want %+v", w.Contributors, wantContribs)
	}
	// Editorial subject IRI merges with the feed subject label.
	wantSubjects := []string{"Fiction", "https://homosaurus.org/v3/homoit0000669"}
	if !reflect.DeepEqual(w.Subjects, wantSubjects) {
		t.Errorf("subjects = %v, want %v", w.Subjects, wantSubjects)
	}
	if !reflect.DeepEqual(w.Languages, []string{"eng"}) {
		t.Errorf("languages = %v", w.Languages)
	}
	if !reflect.DeepEqual(w.Classifications, []string{"FIC073000"}) {
		t.Errorf("classifications = %v", w.Classifications)
	}
	if len(w.Instances) != 1 {
		t.Fatalf("got %d instances, want 1", len(w.Instances))
	}
	inst := w.Instances[0]
	if inst.ID != "i1" || !reflect.DeepEqual(inst.ISBNs, []string{"9781668128251"}) ||
		!reflect.DeepEqual(inst.ProviderIDs, []ProviderID{{Source: "overdrive", Value: "11682058"}}) {
		t.Errorf("instance = %+v", inst)
	}
}

func TestFacets(t *testing.T) {
	cat, err := Project([]byte(sampleCatalog), "overdrive")
	if err != nil {
		t.Fatalf("Project: %v", err)
	}
	f := cat.Facets()
	if f.Version != SchemaVersion {
		t.Errorf("facets version = %d, want %d", f.Version, SchemaVersion)
	}
	if !reflect.DeepEqual(f.Languages, []FacetValue{{Value: "eng", Count: 1}}) {
		t.Errorf("language facet = %+v", f.Languages)
	}
	if !reflect.DeepEqual(f.Contributors, []FacetValue{
		{Value: "Byron, Grace", Count: 1}, {Value: "Endres, Nicky", Count: 1},
	}) {
		t.Errorf("contributor facet = %+v", f.Contributors)
	}
	// Both the feed subject and the editorial subject IRI are faceted.
	if len(f.Subjects) != 2 {
		t.Errorf("subject facet = %+v, want 2 values", f.Subjects)
	}
}

// catalogWithMerges is a catalog.nq whose editorial graph records a merge chain
// (a->b->c) and an independent merge (d->e), plus a feed line that must be ignored.
const catalogWithMerges = `<#aWork> <https://github.com/freeeve/libcatalog/ns#mergedInto> <#bWork> <editorial:> .
<#bWork> <https://github.com/freeeve/libcatalog/ns#mergedInto> <#cWork> <editorial:> .
<#dWork> <https://github.com/freeeve/libcatalog/ns#mergedInto> <#eWork> <editorial:> .
<#cWork> <http://www.w3.org/1999/02/22-rdf-syntax-ns#type> <http://id.loc.gov/ontologies/bibframe/Work> <feed:overdrive> .
`

func TestRedirects(t *testing.T) {
	rm, err := Redirects([]byte(catalogWithMerges))
	if err != nil {
		t.Fatalf("Redirects: %v", err)
	}
	if rm.Version != SchemaVersion {
		t.Errorf("version = %d, want %d", rm.Version, SchemaVersion)
	}
	// Chain a->b->c collapses to a->c and b->c; d->e independent. Sorted by From.
	want := []Redirect{{From: "a", To: "c"}, {From: "b", To: "c"}, {From: "d", To: "e"}}
	if !reflect.DeepEqual(rm.Redirects, want) {
		t.Errorf("redirects = %+v, want %+v", rm.Redirects, want)
	}
}

func TestRedirectsCycleSafe(t *testing.T) {
	// A malformed overlay with a cycle (a<->b) must terminate, not loop, and emit no
	// self-redirect (From == To is dropped).
	cyc := `<#aWork> <https://github.com/freeeve/libcatalog/ns#mergedInto> <#bWork> <editorial:> .
<#bWork> <https://github.com/freeeve/libcatalog/ns#mergedInto> <#aWork> <editorial:> .
`
	rm, err := Redirects([]byte(cyc))
	if err != nil {
		t.Fatalf("Redirects: %v", err)
	}
	for _, r := range rm.Redirects {
		if r.From == r.To {
			t.Errorf("self-redirect emitted: %+v", r)
		}
	}
}
