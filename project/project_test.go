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
`

func TestProject(t *testing.T) {
	cat, err := Project([]byte(sampleCatalog), "overdrive")
	if err != nil {
		t.Fatalf("Project: %v", err)
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
		!reflect.DeepEqual(inst.ProviderIDs, []string{"11682058"}) {
		t.Errorf("instance = %+v", inst)
	}
}
