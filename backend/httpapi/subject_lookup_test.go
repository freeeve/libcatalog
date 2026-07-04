package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	codex "github.com/freeeve/libcodex"

	"github.com/freeeve/libcatalog/bibframe"
	"github.com/freeeve/libcatalog/storage/blob"

	"github.com/freeeve/libcatalog/backend/auth"
	"github.com/freeeve/libcatalog/backend/copycat"
	"github.com/freeeve/libcatalog/backend/store"
	"github.com/freeeve/libcatalog/backend/vocab"
)

const lookupVocabNT = `<https://homosaurus.org/v4/homoit1> <http://www.w3.org/2004/02/skos/core#prefLabel> "Gay men"@en <authority:homosaurus> .
<http://www.wikidata.org/entity/Q322481> <http://www.w3.org/2004/02/skos/core#prefLabel> "revenge"@en <authority:wikidata> .
<http://www.wikidata.org/entity/Q322481> <http://www.w3.org/2004/02/skos/core#exactMatch> <https://d-nb.info/gnd/4048394-2> <authority:wikidata> .
`

// marcWithSubjects fabricates one external hit carrying 650s and a 655.
func marcWithSubjects() *codex.Record {
	rec := codex.NewRecord()
	rec.AddField(codex.NewControlField("001", "X1"))
	rec.AddField(codex.NewDataField("650", ' ', '0', codex.NewSubfield('a', "Gay men"), codex.NewSubfield('x', "History.")))
	rec.AddField(codex.NewDataField("650", ' ', '0', codex.NewSubfield('a', "Gay men.")))
	rec.AddField(codex.NewDataField("650", ' ', '2', codex.NewSubfield('a', "Sexual Behavior")))
	rec.AddField(codex.NewDataField("655", ' ', '7', codex.NewSubfield('a', "Essays."), codex.NewSubfield('2', "lcgft")))
	// A German GND heading whose $0 identifier crosswalks to the English
	// wikidata term (the label alone never would).
	rec.AddField(codex.NewDataField("650", ' ', '7',
		codex.NewSubfield('a', "Rache"),
		codex.NewSubfield('0', "(DE-588)4048394-2"),
		codex.NewSubfield('2', "gnd")))
	// Already-carried tag on the work: must not come back.
	rec.AddField(codex.NewDataField("650", ' ', '0', codex.NewSubfield('a', "Nonfiction.")))
	return rec
}

func TestSubjectLookupByISBN(t *testing.T) {
	ctx := t.Context()
	bs := blob.NewMem()
	// Vocab: "Gay men" resolves in homosaurus.
	_, _ = bs.Put(ctx, "data/authorities/x.nq", []byte(lookupVocabNT), blob.PutOptions{})
	ix, err := vocab.Load(ctx, bs, "data/authorities/", nil)
	if err != nil {
		t.Fatal(err)
	}
	// Work grain with an ISBN plus an existing "Nonfiction." tag. The
	// summary walk goes work->hasInstance, so assert the forward link too.
	workID := "wlookup00001"
	grain := identityGrain(workID, "A Book", "Author, Some", "9780441478125")
	grain = append(grain, []byte(`<#`+workID+`Work> <http://id.loc.gov/ontologies/bibframe/hasInstance> <#`+workID+`iInstance> <feed:overdrive> .
<#`+workID+`Work> <https://github.com/freeeve/libcatalog/ns#tag> "Nonfiction." <editorial:> .`+"\n")...)
	if _, err := bs.Put(ctx, bibframe.GrainPath(workID), grain, blob.PutOptions{}); err != nil {
		t.Fatal(err)
	}
	db := store.NewMem()
	cc := &copycat.Service{
		Blob: bs, DB: db,
		Search: func(_ context.Context, tgt copycat.Target, terms []copycat.FieldTerm, _ int) ([]*codex.Record, error) {
			if len(terms) != 1 || terms[0].Index != "isbn" || terms[0].Term != "9780441478125" {
				t.Errorf("searched %+v, want the isbn access point", terms)
			}
			return []*codex.Record{marcWithSubjects()}, nil
		},
	}
	if err := cc.PutTarget(ctx, copycat.Target{Name: "loc", URL: "x", Protocol: "sru"}); err != nil {
		t.Fatal(err)
	}
	verifier := staffVerifier{"lib-token": {Email: "lib@example.org", Roles: []auth.Role{auth.RoleLibrarian}}}
	h := New(Deps{Blob: bs, DB: db, Vocab: ix, Copycat: cc, Verifier: verifier})

	rec := request(t, h, http.MethodPost, "/v1/works/"+workID+"/subjects/lookup", "lib-token", "", map[string]any{})
	if rec.Code != http.StatusOK {
		t.Fatalf("lookup = %d %s", rec.Code, rec.Body)
	}
	var res struct {
		Candidates []subjectCandidate `json:"candidates"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	byHeading := map[string]subjectCandidate{}
	for _, c := range res.Candidates {
		byHeading[c.Heading] = c
	}
	// "Gay men" reconciles to the controlled homosaurus term.
	gay, ok := byHeading["Gay men"]
	if !ok || gay.Term == nil || gay.Term.Scheme != "homosaurus" || gay.Count != 1 || gay.Source != "lcsh" {
		t.Fatalf("gay men candidate = %+v (ok=%v)", gay, ok)
	}
	// Subdivided heading survives with the double-dash join.
	if _, ok := byHeading["Gay men--History"]; !ok {
		t.Fatalf("subdivided heading missing: %v", byHeading)
	}
	// The GND heading reconciles through its $0 identifier to the
	// English-labeled wikidata term, despite the German label.
	rache, ok := byHeading["Rache"]
	if !ok || rache.Term == nil || rache.Term.Scheme != "wikidata" || rache.Term.Label != "revenge" ||
		rache.Source != "gnd" || len(rache.IDs) != 1 || rache.IDs[0] != "https://d-nb.info/gnd/4048394-2" {
		t.Fatalf("gnd candidate = %+v (ok=%v)", rache, ok)
	}
	// MeSH and $2 sources carry through; unreconciled headings have no term.
	if c := byHeading["Sexual Behavior"]; c.Source != "mesh" || c.Term != nil {
		t.Fatalf("mesh candidate = %+v", c)
	}
	if c := byHeading["Essays"]; c.Source != "lcgft" {
		t.Fatalf("essays candidate = %+v", c)
	}
	// The already-carried tag is filtered out.
	if _, ok := byHeading["Nonfiction"]; ok {
		t.Fatal("existing tag came back as a candidate")
	}
	// Controlled candidates sort first.
	if res.Candidates[0].Term == nil {
		t.Fatalf("first candidate uncontrolled: %+v", res.Candidates[0])
	}
}

func TestIdentifierKinds(t *testing.T) {
	ctx := t.Context()
	bs := blob.NewMem()
	workID := "widkinds0001"
	if _, err := bs.Put(ctx, bibframe.GrainPath(workID),
		identityGrain(workID, "A Book", "Author, Some", "9780441478125"), blob.PutOptions{}); err != nil {
		t.Fatal(err)
	}
	verifier := staffVerifier{"lib-token": {Email: "lib@example.org", Roles: []auth.Role{auth.RoleLibrarian}}}
	h := New(Deps{Blob: bs, DB: store.NewMem(), Copycat: &copycat.Service{Blob: bs, DB: store.NewMem()}, Verifier: verifier})

	rec := request(t, h, http.MethodGet, "/v1/works/"+workID+"/identifiers", "lib-token", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("identifiers = %d %s", rec.Code, rec.Body)
	}
	var res struct {
		Kinds map[string]string `json:"kinds"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if res.Kinds["9780441478125"] != "isbn" {
		t.Fatalf("kinds = %v", res.Kinds)
	}
}
