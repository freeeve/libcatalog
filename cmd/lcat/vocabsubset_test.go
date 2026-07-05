package main

import (
	"strings"
	"testing"
)

func TestSubsetFromNT(t *testing.T) {
	const uri = "http://id.loc.gov/authorities/subjects/sh85118629"
	// The shape id.loc.gov returns for <uri>.skos.nt: kept SKOS statements plus
	// noise (marcKey) the converter must drop.
	body := strings.Join([]string{
		`<` + uri + `> <http://www.w3.org/2004/02/skos/core#prefLabel> "Science fiction"@en .`,
		`<` + uri + `> <http://www.w3.org/2004/02/skos/core#broader> <http://id.loc.gov/authorities/subjects/sh85048050> .`,
		`<` + uri + `> <http://id.loc.gov/ontologies/bflc/marcKey> "150  $aScience fiction" .`,
		``,
	}, "\n")

	out, terms := subsetFromNT("lcsh", []string{uri, "http://id.loc.gov/authorities/subjects/shMISSING"},
		map[string][]byte{uri: []byte(body)})
	s := string(out)

	if terms != 1 {
		t.Fatalf("terms = %d, want 1 (one prefLabel-bearing concept)", terms)
	}
	if !strings.Contains(s, "<authority:lcsh>") {
		t.Fatalf("output not re-graphed to authority:lcsh:\n%s", s)
	}
	if !strings.Contains(s, `"Science fiction"@en`) {
		t.Fatalf("prefLabel dropped:\n%s", s)
	}
	if !strings.Contains(s, "sh85048050") {
		t.Fatalf("broader dropped:\n%s", s)
	}
	if strings.Contains(s, "marcKey") {
		t.Fatalf("non-SKOS noise kept:\n%s", s)
	}
	// The missing URI (no fetched body) is simply skipped, not fatal.
	if strings.Contains(s, "shMISSING") {
		t.Fatalf("emitted a term with no fetched concept:\n%s", s)
	}
}
