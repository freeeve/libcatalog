package project

import (
	"reflect"
	"sort"
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
_:cl <http://www.w3.org/2000/01/rdf-schema#label> "Fiction / LGBTQ+ / Transgender" <feed:overdrive> .
<#w1Work> <http://id.loc.gov/ontologies/bibframe/classification> _:cl2 <feed:overdrive> .
_:cl2 <http://id.loc.gov/ontologies/bibframe/classificationPortion> "FIC027000" <feed:overdrive> .
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
<#i1Instance> <http://id.loc.gov/ontologies/bibframe/media> _:m1 <feed:overdrive> .
_:m1 <http://www.w3.org/1999/02/22-rdf-syntax-ns#type> <http://id.loc.gov/ontologies/bibframe/Media> <feed:overdrive> .
_:m1 <http://www.w3.org/2000/01/rdf-schema#label> "computer" <feed:overdrive> .
<https://homosaurus.org/v3/homoit0000669> <http://www.w3.org/2004/02/skos/core#prefLabel> "Transgender people"@en <authority:homosaurus> .
<https://homosaurus.org/v3/homoit0000669> <http://www.w3.org/2004/02/skos/core#prefLabel> "Personas trans"@es <authority:homosaurus> .
<https://homosaurus.org/v3/homoit0000669> <http://www.w3.org/2004/02/skos/core#broader> <https://homosaurus.org/v3/homoit0000282> <authority:homosaurus> .
<https://homosaurus.org/v3/homoit0000282> <http://www.w3.org/2004/02/skos/core#prefLabel> "Gender identity"@en <authority:homosaurus> .
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
	// The editorial IRI subject is controlled -- carried as {URI, resolved labels};
	// the feed genre string is an uncontrolled tag. They no longer conflate (tasks/012).
	wantSubjects := []Subject{{
		ID:      "https://homosaurus.org/v3/homoit0000669",
		Labels:  map[string]string{"en": "Transgender people", "es": "Personas trans"},
		Broader: []string{"https://homosaurus.org/v3/homoit0000282"},
		Scheme:  "homosaurus",
	}}
	if !reflect.DeepEqual(w.Subjects, wantSubjects) {
		t.Errorf("subjects = %+v, want %+v", w.Subjects, wantSubjects)
	}
	if !reflect.DeepEqual(w.Tags, []string{"Fiction"}) {
		t.Errorf("tags = %v, want [Fiction]", w.Tags)
	}
	if !reflect.DeepEqual(w.Languages, []string{"eng"}) {
		t.Errorf("languages = %v", w.Languages)
	}
	// A classification is {code, optional rdfs:label} (tasks/142): the labeled
	// node carries its display text, the bare one falls back to code-only.
	wantClassifications := []Classification{
		{Value: "FIC027000"},
		{Value: "FIC073000", Label: "Fiction / LGBTQ+ / Transgender"},
	}
	if !reflect.DeepEqual(w.Classifications, wantClassifications) {
		t.Errorf("classifications = %+v, want %+v", w.Classifications, wantClassifications)
	}
	if len(w.Instances) != 1 {
		t.Fatalf("got %d instances, want 1", len(w.Instances))
	}
	inst := w.Instances[0]
	if inst.ID != "i1" || inst.Format != "ebook" || !reflect.DeepEqual(inst.ISBNs, []string{"9781668128251"}) ||
		!reflect.DeepEqual(inst.ProviderIDs, []ProviderID{{Source: "overdrive", Value: "11682058"}}) {
		t.Errorf("instance = %+v", inst)
	}
	// The Instance's RDA media type "computer" projects to the ebook format, and the
	// Work's formats facet is the union of its Instances' formats (tasks/011).
	if !reflect.DeepEqual(w.Formats, []string{"ebook"}) {
		t.Errorf("formats = %v, want [ebook]", w.Formats)
	}
}

// clusteredFormats is one Work with two Instances -- an ebook (media "computer") and
// an audiobook (media "audio") -- the edition-clustering case tasks/011 targets.
const clusteredFormats = `<#w9Work> <http://www.w3.org/1999/02/22-rdf-syntax-ns#type> <http://id.loc.gov/ontologies/bibframe/Work> <feed:overdrive> .
<#w9Work> <http://id.loc.gov/ontologies/bibframe/hasInstance> <#i9aInstance> <feed:overdrive> .
<#w9Work> <http://id.loc.gov/ontologies/bibframe/hasInstance> <#i9bInstance> <feed:overdrive> .
<#i9aInstance> <http://www.w3.org/1999/02/22-rdf-syntax-ns#type> <http://id.loc.gov/ontologies/bibframe/Instance> <feed:overdrive> .
<#i9aInstance> <http://id.loc.gov/ontologies/bibframe/media> _:ma <feed:overdrive> .
_:ma <http://www.w3.org/2000/01/rdf-schema#label> "computer" <feed:overdrive> .
<#i9bInstance> <http://www.w3.org/1999/02/22-rdf-syntax-ns#type> <http://id.loc.gov/ontologies/bibframe/Instance> <feed:overdrive> .
<#i9bInstance> <http://id.loc.gov/ontologies/bibframe/media> _:mb <feed:overdrive> .
_:mb <http://www.w3.org/2000/01/rdf-schema#label> "audio" <feed:overdrive> .
`

// TestClusteredFormats is the tasks/011 acceptance: a clustered ebook+audiobook Work
// exposes both formats via its Instances, and both appear in the Work-level union and
// the formats facet.
func TestClusteredFormats(t *testing.T) {
	cat, err := Project([]byte(clusteredFormats), "overdrive")
	if err != nil {
		t.Fatalf("Project: %v", err)
	}
	if len(cat.Works) != 1 {
		t.Fatalf("got %d works, want 1", len(cat.Works))
	}
	w := cat.Works[0]
	byID := map[string]string{}
	for _, inst := range w.Instances {
		byID[inst.ID] = inst.Format
	}
	if byID["i9a"] != "ebook" || byID["i9b"] != "audiobook" {
		t.Errorf("instance formats = %+v, want i9a=ebook i9b=audiobook", byID)
	}
	// Union is sorted: audiobook before ebook.
	if !reflect.DeepEqual(w.Formats, []string{"audiobook", "ebook"}) {
		t.Errorf("work formats = %v, want [audiobook ebook]", w.Formats)
	}
	f := cat.Facets()
	if !reflect.DeepEqual(f.Formats, []FacetValue{{Value: "audiobook", Count: 1}, {Value: "ebook", Count: 1}}) {
		t.Errorf("format facet = %+v", f.Formats)
	}
}

// contribTieBreak has two non-primary contributions by the same agent, listed
// role "editor" before "author" -- so a sort that falls back to statement order
// would emit them in that order. A total order (by role) must not.
const contribTieBreak = `<#w2Work> <http://www.w3.org/1999/02/22-rdf-syntax-ns#type> <http://id.loc.gov/ontologies/bibframe/Work> <feed:overdrive> .
<#w2Work> <http://id.loc.gov/ontologies/bibframe/contribution> _:cA <feed:overdrive> .
_:cA <http://id.loc.gov/ontologies/bibframe/agent> _:agA <feed:overdrive> .
_:agA <http://www.w3.org/2000/01/rdf-schema#label> "Doe, Sam" <feed:overdrive> .
_:cA <http://id.loc.gov/ontologies/bibframe/role> _:rA <feed:overdrive> .
_:rA <http://www.w3.org/2000/01/rdf-schema#label> "editor" <feed:overdrive> .
<#w2Work> <http://id.loc.gov/ontologies/bibframe/contribution> _:cB <feed:overdrive> .
_:cB <http://id.loc.gov/ontologies/bibframe/agent> _:agB <feed:overdrive> .
_:agB <http://www.w3.org/2000/01/rdf-schema#label> "Doe, Sam" <feed:overdrive> .
_:cB <http://id.loc.gov/ontologies/bibframe/role> _:rB <feed:overdrive> .
_:rB <http://www.w3.org/2000/01/rdf-schema#label> "author" <feed:overdrive> .
`

// TestContributorsDeterministicOrder guards projection determinism: contributions
// sharing an agent name must sort by role, not by graph statement order, so two
// equivalent serializations project identically (surfaced by lcat serialize).
func TestContributorsDeterministicOrder(t *testing.T) {
	cat, err := Project([]byte(contribTieBreak), "overdrive")
	if err != nil {
		t.Fatalf("Project: %v", err)
	}
	if len(cat.Works) != 1 {
		t.Fatalf("got %d works, want 1", len(cat.Works))
	}
	want := []Contributor{{Name: "Doe, Sam", Role: "author"}, {Name: "Doe, Sam", Role: "editor"}}
	if !reflect.DeepEqual(cat.Works[0].Contributors, want) {
		t.Errorf("contributors = %+v, want role-sorted %+v", cat.Works[0].Contributors, want)
	}
}

// contribDuplicate repeats the same (agent, role) on two contribution nodes --
// a feed + editorial re-assertion, or a provider repeating a creator.
const contribDuplicate = `<#w3Work> <http://www.w3.org/1999/02/22-rdf-syntax-ns#type> <http://id.loc.gov/ontologies/bibframe/Work> <feed:overdrive> .
<#w3Work> <http://id.loc.gov/ontologies/bibframe/contribution> _:cA <feed:overdrive> .
_:cA <http://id.loc.gov/ontologies/bibframe/agent> _:agA <feed:overdrive> .
_:agA <http://www.w3.org/2000/01/rdf-schema#label> "Doe, Sam" <feed:overdrive> .
_:cA <http://id.loc.gov/ontologies/bibframe/role> _:rA <feed:overdrive> .
_:rA <http://www.w3.org/2000/01/rdf-schema#label> "author" <feed:overdrive> .
<#w3Work> <http://id.loc.gov/ontologies/bibframe/contribution> _:cB <editorial:> .
_:cB <http://id.loc.gov/ontologies/bibframe/agent> _:agB <editorial:> .
_:agB <http://www.w3.org/2000/01/rdf-schema#label> "Doe, Sam" <editorial:> .
_:cB <http://id.loc.gov/ontologies/bibframe/role> _:rB <editorial:> .
_:rB <http://www.w3.org/2000/01/rdf-schema#label> "author" <editorial:> .
`

// TestContributorsDeduped covers tasks/115: two contribution nodes carrying
// the same (name, role) -- e.g. a feed and an editorial re-assertion --
// project as one contributor, like every other deduped dimension.
func TestContributorsDeduped(t *testing.T) {
	cat, err := Project([]byte(contribDuplicate), "overdrive")
	if err != nil {
		t.Fatalf("Project: %v", err)
	}
	if len(cat.Works) != 1 {
		t.Fatalf("got %d works, want 1", len(cat.Works))
	}
	want := []Contributor{{Name: "Doe, Sam", Role: "author"}}
	if !reflect.DeepEqual(cat.Works[0].Contributors, want) {
		t.Errorf("contributors = %+v, want deduped %+v", cat.Works[0].Contributors, want)
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
	// Controlled subjects and feed tags facet separately (tasks/012): the Homosaurus
	// URI is the sole subject facet (with resolved labels), "Fiction" the sole tag.
	wantSubj := []SubjectFacet{{
		ID:      "https://homosaurus.org/v3/homoit0000669",
		Labels:  map[string]string{"en": "Transgender people", "es": "Personas trans"},
		Broader: []string{"https://homosaurus.org/v3/homoit0000282"},
		Scheme:  "homosaurus",
		Count:   1,
	}}
	if !reflect.DeepEqual(f.Subjects, wantSubj) {
		t.Errorf("subject facet = %+v, want %+v", f.Subjects, wantSubj)
	}
	if !reflect.DeepEqual(f.Tags, []FacetValue{{Value: "Fiction", Count: 1}}) {
		t.Errorf("tag facet = %+v, want [{Fiction 1}]", f.Tags)
	}
	if !reflect.DeepEqual(f.Formats, []FacetValue{{Value: "ebook", Count: 1}}) {
		t.Errorf("format facet = %+v, want [{ebook 1}]", f.Formats)
	}
	// The classification facet keys on the code and carries the display label
	// when the graph has one (tasks/142).
	wantCls := []ClassificationFacet{
		{Value: "FIC027000", Count: 1},
		{Value: "FIC073000", Label: "Fiction / LGBTQ+ / Transgender", Count: 1},
	}
	if !reflect.DeepEqual(f.Classifications, wantCls) {
		t.Errorf("classification facet = %+v, want %+v", f.Classifications, wantCls)
	}
}

// catalogWithMerges is a catalog.nq whose editorial graph records a merge chain
// (a->b->c) and an independent merge (d->e), plus a feed line that must be ignored.
const catalogWithMerges = `<#aWork> <https://github.com/freeeve/libcat/ns#mergedInto> <#bWork> <editorial:> .
<#bWork> <https://github.com/freeeve/libcat/ns#mergedInto> <#cWork> <editorial:> .
<#dWork> <https://github.com/freeeve/libcat/ns#mergedInto> <#eWork> <editorial:> .
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
	cyc := `<#aWork> <https://github.com/freeeve/libcat/ns#mergedInto> <#bWork> <editorial:> .
<#bWork> <https://github.com/freeeve/libcat/ns#mergedInto> <#aWork> <editorial:> .
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

// TestSubjectBroader covers the skos:broader projection (tasks/015): a term's parents
// are emitted sorted + deduped, non-IRI broader objects are ignored, and a subject
// with no broader omits the field.
func TestSubjectBroader(t *testing.T) {
	const nq = `<#waWork> <http://www.w3.org/1999/02/22-rdf-syntax-ns#type> <http://id.loc.gov/ontologies/bibframe/Work> <feed:overdrive> .
<#waWork> <http://id.loc.gov/ontologies/bibframe/subject> <https://ex.org/child> <feed:overdrive> .
<#wbWork> <http://www.w3.org/1999/02/22-rdf-syntax-ns#type> <http://id.loc.gov/ontologies/bibframe/Work> <feed:overdrive> .
<#wbWork> <http://id.loc.gov/ontologies/bibframe/subject> <https://ex.org/orphan> <feed:overdrive> .
<https://ex.org/child> <http://www.w3.org/2004/02/skos/core#broader> <https://ex.org/zparent> <authority:x> .
<https://ex.org/child> <http://www.w3.org/2004/02/skos/core#broader> <https://ex.org/aparent> <authority:x> .
<https://ex.org/child> <http://www.w3.org/2004/02/skos/core#broader> <https://ex.org/aparent> <authority:x> .
<https://ex.org/child> <http://www.w3.org/2004/02/skos/core#broader> _:blanknode <authority:x> .
<https://ex.org/child> <http://www.w3.org/2004/02/skos/core#broader> "literal parent" <authority:x> .
`
	cat, err := Project([]byte(nq), "overdrive")
	if err != nil {
		t.Fatalf("Project: %v", err)
	}
	byID := map[string][]string{}
	for _, w := range cat.Works {
		for _, s := range w.Subjects {
			byID[s.ID] = s.Broader
		}
	}
	// child's parents: deduped (aparent given twice -> once) + sorted (aparent <
	// zparent); the blank-node and literal broader objects are ignored (IRIs only).
	wantChild := []string{"https://ex.org/aparent", "https://ex.org/zparent"}
	if got := byID["https://ex.org/child"]; !reflect.DeepEqual(got, wantChild) {
		t.Errorf("child broader = %v, want %v", got, wantChild)
	}
	// orphan has no skos:broader -> Broader omitted (nil).
	if got, ok := byID["https://ex.org/orphan"]; !ok || got != nil {
		t.Errorf("orphan broader = %v (present=%v), want nil", got, ok)
	}
}

// TestTermSideband covers the vocabulary sideband (tasks/178): Catalog.Terms
// carries every referenced subject plus its transitive skos:broader closure
// when the graph has metadata for them -- including an ancestor chain an
// enricher described with no work link -- and skips URIs the graph says
// nothing about.
func TestTermSideband(t *testing.T) {
	const nq = `<#waWork> <http://www.w3.org/1999/02/22-rdf-syntax-ns#type> <http://id.loc.gov/ontologies/bibframe/Work> <feed:overdrive> .
<#waWork> <http://id.loc.gov/ontologies/bibframe/subject> <https://homosaurus.org/v3/child> <feed:overdrive> .
<https://homosaurus.org/v3/child> <http://www.w3.org/2004/02/skos/core#prefLabel> "Trans women"@en <enrichment:x> .
<https://homosaurus.org/v3/child> <http://www.w3.org/2004/02/skos/core#broader> <https://homosaurus.org/v3/parent> <enrichment:x> .
<https://homosaurus.org/v3/parent> <http://www.w3.org/2004/02/skos/core#prefLabel> "Gender minorities"@en <enrichment:x> .
<https://homosaurus.org/v3/parent> <http://www.w3.org/2004/02/skos/core#broader> <https://homosaurus.org/v3/grand> <enrichment:x> .
<https://homosaurus.org/v3/grand> <http://www.w3.org/2004/02/skos/core#prefLabel> "People"@en <enrichment:x> .
`
	cat, err := Project([]byte(nq), "overdrive")
	if err != nil {
		t.Fatalf("Project: %v", err)
	}
	byID := map[string]Term{}
	for _, term := range cat.Terms {
		byID[term.ID] = term
	}
	if len(cat.Terms) != 3 {
		t.Fatalf("Terms = %+v, want child+parent+grand", cat.Terms)
	}
	if term := byID["https://homosaurus.org/v3/parent"]; term.Labels["en"] != "Gender minorities" ||
		len(term.Broader) != 1 || term.Broader[0] != "https://homosaurus.org/v3/grand" || term.Scheme != "homosaurus" {
		t.Errorf("parent term = %+v", term)
	}
	// grand is reachable only transitively (no work names it, parent does).
	if term, ok := byID["https://homosaurus.org/v3/grand"]; !ok || term.Labels["en"] != "People" {
		t.Errorf("grand term = %+v (present=%v)", term, ok)
	}
	// Sorted by ID for determinism.
	if !sort.SliceIsSorted(cat.Terms, func(i, j int) bool { return cat.Terms[i].ID < cat.Terms[j].ID }) {
		t.Errorf("Terms not sorted: %+v", cat.Terms)
	}
}

// TestTermSidebandSkipsBareURIs: a broader target the graph never describes
// (no labels, no broader of its own) contributes no Terms entry.
func TestTermSidebandSkipsBareURIs(t *testing.T) {
	const nq = `<#waWork> <http://www.w3.org/1999/02/22-rdf-syntax-ns#type> <http://id.loc.gov/ontologies/bibframe/Work> <feed:overdrive> .
<#waWork> <http://id.loc.gov/ontologies/bibframe/subject> <https://ex.org/bare> <feed:overdrive> .
`
	cat, err := Project([]byte(nq), "overdrive")
	if err != nil {
		t.Fatalf("Project: %v", err)
	}
	if cat.Terms != nil {
		t.Errorf("Terms = %+v, want nil (nothing described)", cat.Terms)
	}
}

// TestWorkExtras covers the adopter-extras projection (tasks/026): a Work's
// bibframe.ExtraPred literals in the projected provider's feed graph surface as
// Work.Extra; an extra predicate in another graph is ignored (provenance-scoped); and a
// Work with no extras omits the field.
func TestWorkExtras(t *testing.T) {
	const nq = `<#weWork> <http://www.w3.org/1999/02/22-rdf-syntax-ns#type> <http://id.loc.gov/ontologies/bibframe/Work> <feed:overdrive> .
<#weWork> <https://github.com/freeeve/libcat/ns#extra/cover> "https://covers.example.org/x.jpg" <feed:overdrive> .
<#weWork> <https://github.com/freeeve/libcat/ns#extra/rating> "5" <feed:overdrive> .
<#weWork> <https://github.com/freeeve/libcat/ns#extra/dateRead> "2026-01-15" <feed:overdrive> .
<#weWork> <https://github.com/freeeve/libcat/ns#extra/ignored> "other feed" <feed:other> .
<#wnWork> <http://www.w3.org/1999/02/22-rdf-syntax-ns#type> <http://id.loc.gov/ontologies/bibframe/Work> <feed:overdrive> .
`
	cat, err := Project([]byte(nq), "overdrive")
	if err != nil {
		t.Fatalf("Project: %v", err)
	}
	byID := map[string]map[string]string{}
	for _, w := range cat.Works {
		byID[w.ID] = w.Extra
	}
	want := map[string]string{
		"cover":    "https://covers.example.org/x.jpg",
		"rating":   "5",
		"dateRead": "2026-01-15",
	}
	if got := byID["we"]; !reflect.DeepEqual(got, want) {
		t.Errorf("we extra = %v, want %v (an extra predicate in feed:other must not leak in)", got, want)
	}
	// A Work with no extras omits the field (nil), so a catalog without extras is unchanged.
	if got := byID["wn"]; got != nil {
		t.Errorf("wn extra = %v, want nil", got)
	}
}

// TestWorkSummary covers the bf:summary projection (tasks/124): the label of the
// Work's first labeled bf:Summary node surfaces as Work.Summary; an unlabeled
// summary node is skipped in favor of a later labeled one; a Work without a
// summary omits the field.
func TestWorkSummary(t *testing.T) {
	const nq = `<#wsWork> <http://www.w3.org/1999/02/22-rdf-syntax-ns#type> <http://id.loc.gov/ontologies/bibframe/Work> <feed:overdrive> .
<#wsWork> <http://id.loc.gov/ontologies/bibframe/summary> _:empty <feed:overdrive> .
<#wsWork> <http://id.loc.gov/ontologies/bibframe/summary> _:s1 <feed:overdrive> .
_:empty <http://www.w3.org/1999/02/22-rdf-syntax-ns#type> <http://id.loc.gov/ontologies/bibframe/Summary> <feed:overdrive> .
_:s1 <http://www.w3.org/1999/02/22-rdf-syntax-ns#type> <http://id.loc.gov/ontologies/bibframe/Summary> <feed:overdrive> .
_:s1 <http://www.w3.org/2000/01/rdf-schema#label> "A haunting debut novel." <feed:overdrive> .
<#wnWork> <http://www.w3.org/1999/02/22-rdf-syntax-ns#type> <http://id.loc.gov/ontologies/bibframe/Work> <feed:overdrive> .
`
	cat, err := Project([]byte(nq), "overdrive")
	if err != nil {
		t.Fatalf("Project: %v", err)
	}
	byID := map[string]string{}
	for _, w := range cat.Works {
		byID[w.ID] = w.Summary
	}
	if got := byID["ws"]; got != "A haunting debut novel." {
		t.Errorf("ws summary = %q, want %q", got, "A haunting debut novel.")
	}
	if got := byID["wn"]; got != "" {
		t.Errorf("wn summary = %q, want empty (omitted)", got)
	}
}

// TestRelationsAndSeries covers tasks/222 (schema v11): editorial
// whole/part links project as {id, title} restricted to the projection --
// a link to a suppressed work is dropped -- and instance series
// statement/enumeration carry through.
func TestRelationsAndSeries(t *testing.T) {
	const nq = `<#waaWork> <http://www.w3.org/1999/02/22-rdf-syntax-ns#type> <http://id.loc.gov/ontologies/bibframe/Work> <feed:overdrive> .
<#waaWork> <http://id.loc.gov/ontologies/bibframe/title> _:ta <feed:overdrive> .
_:ta <http://id.loc.gov/ontologies/bibframe/mainTitle> "The Whole" <feed:overdrive> .
<#waaWork> <http://id.loc.gov/ontologies/bibframe/hasInstance> <#iaaInstance> <feed:overdrive> .
<#iaaInstance> <http://id.loc.gov/ontologies/bibframe/instanceOf> <#waaWork> <feed:overdrive> .
<#iaaInstance> <http://id.loc.gov/ontologies/bibframe/seriesStatement> "Big Series" <editorial:> .
<#iaaInstance> <http://id.loc.gov/ontologies/bibframe/seriesEnumeration> "v. 2" <editorial:> .
<#waaWork> <http://id.loc.gov/ontologies/bibframe/hasPart> <#wbbWork> <editorial:> .
<#waaWork> <http://id.loc.gov/ontologies/bibframe/hasPart> <#whiddenWork> <editorial:> .
<#wbbWork> <http://www.w3.org/1999/02/22-rdf-syntax-ns#type> <http://id.loc.gov/ontologies/bibframe/Work> <feed:overdrive> .
<#wbbWork> <http://id.loc.gov/ontologies/bibframe/title> _:tb <feed:overdrive> .
_:tb <http://id.loc.gov/ontologies/bibframe/mainTitle> "The Part" <feed:overdrive> .
<#wbbWork> <http://id.loc.gov/ontologies/bibframe/partOf> <#waaWork> <editorial:> .
<#whiddenWork> <http://www.w3.org/1999/02/22-rdf-syntax-ns#type> <http://id.loc.gov/ontologies/bibframe/Work> <feed:overdrive> .
<#whiddenWork> <https://github.com/freeeve/libcat/ns#suppressed> "true" <editorial:> .
`
	cat, err := Project([]byte(nq), "overdrive")
	if err != nil {
		t.Fatalf("Project: %v", err)
	}
	byID := map[string]Work{}
	for _, w := range cat.Works {
		byID[w.ID] = w
	}
	whole := byID["waa"]
	if whole.Relations == nil || len(whole.Relations.HasPart) != 1 ||
		whole.Relations.HasPart[0] != (RelatedWork{ID: "wbb", Title: "The Part"}) {
		t.Fatalf("whole relations = %+v (suppressed target must drop, title must resolve)", whole.Relations)
	}
	part := byID["wbb"]
	if part.Relations == nil || len(part.Relations.PartOf) != 1 ||
		part.Relations.PartOf[0] != (RelatedWork{ID: "waa", Title: "The Whole"}) {
		t.Fatalf("part relations = %+v", part.Relations)
	}
	inst := whole.Instances[0]
	if !reflect.DeepEqual(inst.Series, []string{"Big Series"}) || inst.SeriesEnumeration != "v. 2" {
		t.Fatalf("series = %v / %q", inst.Series, inst.SeriesEnumeration)
	}
	if _, ok := byID["whidden"]; ok {
		t.Fatal("suppressed work projected")
	}
}

// TestSkolemSubjectIsTag covers tasks/218: a labeled grain-local fragment
// node under bf:subject (the editor's -ed- skolem write shape) projects as
// an uncontrolled tag, never as a controlled subject with a forged URI.
func TestSkolemSubjectIsTag(t *testing.T) {
	const nq = `<#wkWork> <http://www.w3.org/1999/02/22-rdf-syntax-ns#type> <http://id.loc.gov/ontologies/bibframe/Work> <feed:overdrive> .
<#wkWork> <http://id.loc.gov/ontologies/bibframe/subject> <#wkWork-ed-subjectLabels> <editorial:> .
<#wkWork-ed-subjectLabels> <http://www.w3.org/2000/01/rdf-schema#label> "Space necromancers" <editorial:> .
`
	cat, err := Project([]byte(nq), "overdrive")
	if err != nil {
		t.Fatalf("Project: %v", err)
	}
	if len(cat.Works) != 1 {
		t.Fatalf("got %d works, want 1", len(cat.Works))
	}
	w := cat.Works[0]
	if len(w.Subjects) != 0 {
		t.Errorf("skolem heading forged controlled subjects: %+v", w.Subjects)
	}
	if !reflect.DeepEqual(w.Tags, []string{"Space necromancers"}) {
		t.Errorf("tags = %v, want [Space necromancers]", w.Tags)
	}
}
