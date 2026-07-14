package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/freeeve/libcat/catalogindex"
)

// sampleCatalogNQ is a miniature catalog.nq: two works sharing an LCSH heading,
// each with one single-use heading in a different scheme.
const sampleCatalogNQ = `<#w1Work> <http://www.w3.org/1999/02/22-rdf-syntax-ns#type> <http://id.loc.gov/ontologies/bibframe/Work> <feed:test> .
<#w2Work> <http://www.w3.org/1999/02/22-rdf-syntax-ns#type> <http://id.loc.gov/ontologies/bibframe/Work> <feed:test> .
<#w1Work> <http://id.loc.gov/ontologies/bibframe/subject> <http://id.loc.gov/authorities/subjects/sh85021262> <editorial:> .
<#w1Work> <http://id.loc.gov/ontologies/bibframe/subject> <https://homosaurus.org/v5/hmit001> <feed:test> .
<#w2Work> <http://id.loc.gov/ontologies/bibframe/subject> <http://id.loc.gov/authorities/subjects/sh85021262> <feed:test> .
<#w2Work> <http://id.loc.gov/ontologies/bibframe/subject> <http://id.worldcat.org/fast/fst123> <feed:test> .
<http://id.loc.gov/authorities/subjects/sh85021262> <http://www.w3.org/2004/02/skos/core#prefLabel> "Cats"@en <authority:lcsh> .
<https://homosaurus.org/v5/hmit001> <http://www.w3.org/2004/02/skos/core#prefLabel> "Queer people"@en <authority:homosaurus> .
<http://id.worldcat.org/fast/fst123> <http://www.w3.org/2000/01/rdf-schema#label> "Widgets"@en <authority:fast> .
`

func writeCatalogNQ(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "catalog.nq")
	if err := os.WriteFile(p, []byte(sampleCatalogNQ), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestRunAuthoritiesJSON exercises the command end to end: it loads a catalog.nq,
// tallies usage, and writes the JSON report the positional-path form produces.
func TestRunAuthoritiesJSON(t *testing.T) {
	path := writeCatalogNQ(t)
	outPath := filepath.Join(t.TempDir(), "authorities.json")
	if err := runAuthorities([]string{path, "--format", "json", "--out", outPath}); err != nil {
		t.Fatalf("runAuthorities: %v", err)
	}
	data, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	var got []catalogindex.AuthorityUse
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0].Works != 2 || got[0].Scheme != "lcsh" {
		t.Fatalf("report = %+v, want the shared LCSH heading first with 2 works", got)
	}
}

// TestRunAuthoritiesRequiresPath rejects invocation with no dataset.
func TestRunAuthoritiesRequiresPath(t *testing.T) {
	if err := runAuthorities([]string{"--scheme", "lcsh"}); err == nil {
		t.Fatal("expected an error when no catalog.nq path is given")
	}
}

// TestFilterAuthorities pins the scheme and work-count window, including
// --max-works 1 as the single-use-heading filter.
func TestFilterAuthorities(t *testing.T) {
	usage := []catalogindex.AuthorityUse{
		{URI: "a", Scheme: "lcsh", Works: 3},
		{URI: "b", Scheme: "fast", Works: 1},
		{URI: "c", Scheme: "lcsh", Works: 1},
	}
	if got := filterAuthorities(usage, "lcsh", 0, 0); len(got) != 2 {
		t.Fatalf("scheme filter kept %d, want 2", len(got))
	}
	single := filterAuthorities(usage, "", 0, 1)
	if len(single) != 2 || single[0].URI != "b" || single[1].URI != "c" {
		t.Fatalf("--max-works 1 = %+v, want the two single-use headings", single)
	}
	if got := filterAuthorities(usage, "", 2, 0); len(got) != 1 || got[0].URI != "a" {
		t.Fatalf("--min-works 2 = %+v, want only a", got)
	}
}
