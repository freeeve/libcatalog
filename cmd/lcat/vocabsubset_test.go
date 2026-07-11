package main

import (
	"strings"
	"testing"

	"github.com/freeeve/libcat/project"
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

	const ns = "http://id.loc.gov/authorities/subjects/"
	out, terms := subsetFromNT("lcsh", ns, []string{uri, "http://id.loc.gov/authorities/subjects/shMISSING"},
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

// TestSubsetFromNTHTTPS covers an https catalog URI must still count
// and must be emitted under https, matching the exact-match index -- id.loc.gov
// serves the concept keyed on the canonical http URI.
func TestSubsetFromNTHTTPS(t *testing.T) {
	const httpsURI = "https://id.loc.gov/authorities/subjects/sh85118629"
	const canon = "http://id.loc.gov/authorities/subjects/sh85118629"
	const ns = "https://id.loc.gov/authorities/subjects/"
	// The fetched payload uses the canonical http URI; a broader in-namespace and
	// an out-of-namespace exactMatch (wikidata) exercise the re-scheme rule.
	body := strings.Join([]string{
		`<` + canon + `> <http://www.w3.org/2004/02/skos/core#prefLabel> "Science fiction"@en .`,
		`<` + canon + `> <http://www.w3.org/2004/02/skos/core#broader> <http://id.loc.gov/authorities/subjects/sh85048050> .`,
		`<` + canon + `> <http://www.w3.org/2004/02/skos/core#closeMatch> <http://www.wikidata.org/entity/Q24925> .`,
		``,
	}, "\n")

	out, terms := subsetFromNT("lcsh", ns, []string{httpsURI}, map[string][]byte{httpsURI: []byte(body)})
	s := string(out)

	if terms != 1 {
		t.Fatalf("terms = %d, want 1 (https URI must still count)", terms)
	}
	// The concept and its in-namespace broader are emitted under https.
	if !strings.Contains(s, "<"+httpsURI+">") {
		t.Fatalf("concept not re-schemed to the catalog's https URI:\n%s", s)
	}
	if !strings.Contains(s, "<https://id.loc.gov/authorities/subjects/sh85048050>") {
		t.Fatalf("in-namespace broader not re-schemed to https:\n%s", s)
	}
	// The canonical http concept URI must not leak through.
	if strings.Contains(s, "<"+canon+">") {
		t.Fatalf("canonical http URI leaked; index would not match:\n%s", s)
	}
	// An out-of-namespace URI keeps its own scheme.
	if !strings.Contains(s, "<http://www.wikidata.org/entity/Q24925>") {
		t.Fatalf("out-of-namespace URI wrongly re-schemed:\n%s", s)
	}
}

// TestConceptURL covers the per-term fetch suffix: id.loc.gov's
// .skos.nt convention stays the default; Homosaurus-style plain .nt works via
// --fetch-suffix; http URIs are fetched over https either way.
func TestConceptURL(t *testing.T) {
	if got := conceptURL("http://id.loc.gov/authorities/subjects/sh85118629", ".skos.nt"); got != "https://id.loc.gov/authorities/subjects/sh85118629.skos.nt" {
		t.Errorf("conceptURL default = %q", got)
	}
	if got := conceptURL("https://homosaurus.org/v3/homoit0000027", ".nt"); got != "https://homosaurus.org/v3/homoit0000027.nt" {
		t.Errorf("conceptURL homosaurus = %q", got)
	}
}

// homosaurusDump is a Homosaurus-shaped whole-vocabulary dump: three
// concepts with prefLabels and hierarchy, plus non-SKOS noise, plus a subject
// outside the namespace that must never be kept.
const homosaurusDump = `<https://homosaurus.org/v3/homoit0000027> <http://www.w3.org/2004/02/skos/core#prefLabel> "Aromantic"@en .
<https://homosaurus.org/v3/homoit0000027> <http://www.w3.org/2004/02/skos/core#broader> <https://homosaurus.org/v3/homoit0000080> .
<https://homosaurus.org/v3/homoit0000027> <http://purl.org/dc/terms/identifier> "homoit0000027" .
<https://homosaurus.org/v3/homoit0000080> <http://www.w3.org/2004/02/skos/core#prefLabel> "Asexual spectrum"@en .
<https://homosaurus.org/v3/homoit0000556> <http://www.w3.org/2004/02/skos/core#prefLabel> "Lesbians"@en .
<http://www.wikidata.org/entity/Q24925> <http://www.w3.org/2004/02/skos/core#prefLabel> "science fiction"@en .
`

// TestSubsetFromDump covers the dump filter mode: only the catalog's concepts
// are kept (plus their kept predicates), noise predicates and other concepts
// are dropped, and the count reflects prefLabel-bearing kept concepts.
func TestSubsetFromDump(t *testing.T) {
	const ns = "https://homosaurus.org/v3/"
	uris := []string{"https://homosaurus.org/v3/homoit0000027"}
	out, terms, err := subsetFromDump("homosaurus", ns, uris, false, []byte(homosaurusDump))
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	if terms != 1 {
		t.Fatalf("terms = %d, want 1", terms)
	}
	if !strings.Contains(s, "<authority:homosaurus>") {
		t.Fatalf("output not graph-tagged authority:homosaurus:\n%s", s)
	}
	if !strings.Contains(s, `"Aromantic"@en`) || !strings.Contains(s, "homoit0000080") {
		t.Fatalf("kept concept's prefLabel/broader missing:\n%s", s)
	}
	if strings.Contains(s, "identifier") {
		t.Fatalf("non-SKOS noise kept:\n%s", s)
	}
	if strings.Contains(s, `"Lesbians"@en`) || strings.Contains(s, "Q24925") {
		t.Fatalf("concepts outside the catalog's slice kept without --all:\n%s", s)
	}
}

// TestSubsetFromDumpAll covers --dump --all: the entire in-namespace vocabulary
// is kept (no catalog needed), out-of-namespace subjects are still dropped, and
// a catalog URI form still wins for concepts the catalog carries.
func TestSubsetFromDumpAll(t *testing.T) {
	const ns = "https://homosaurus.org/v3/"
	// The catalog carries the http form of one concept: its form must win.
	uris := []string{"http://homosaurus.org/v3/homoit0000556"}
	out, terms, err := subsetFromDump("homosaurus", ns, uris, true, []byte(homosaurusDump))
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	if terms != 3 {
		t.Fatalf("terms = %d, want 3 (all in-namespace prefLabel-bearing concepts)", terms)
	}
	if strings.Contains(s, "Q24925") {
		t.Fatalf("out-of-namespace subject kept:\n%s", s)
	}
	if !strings.Contains(s, "<http://homosaurus.org/v3/homoit0000556>") {
		t.Fatalf("catalog's URI form did not win for its concept:\n%s", s)
	}
	if strings.Contains(s, "<https://homosaurus.org/v3/homoit0000556>") {
		t.Fatalf("dump's URI form leaked for a catalog-carried concept:\n%s", s)
	}
}

// TestSubsetFromDumpEmpty: a dump that yields nothing (corrupt -- the N-Quads
// parser skips malformed lines rather than erroring -- or a wrong --namespace)
// is fatal, unlike per-term fetch skips: the dump is the sole input.
func TestSubsetFromDumpEmpty(t *testing.T) {
	if _, _, err := subsetFromDump("homosaurus", "https://homosaurus.org/v3/", nil, true, []byte("<not nquads")); err == nil {
		t.Fatal("want error for a dump keeping no concepts, got nil")
	}
	if _, _, err := subsetFromDump("homosaurus", "https://example.org/other/", nil, true, []byte(homosaurusDump)); err == nil {
		t.Fatal("want error for a namespace matching nothing, got nil")
	}
}

func TestSubsetFromCatalog(t *testing.T) {
	const ns = "http://id.worldcat.org/fast/"
	works := []project.Work{
		{ID: "w1", Subjects: []project.Subject{
			{ID: ns + "1136767", Labels: map[string]string{"en": "Substance abuse"}},
			{ID: ns + "796510", Labels: map[string]string{"en": "Lesbians", "es": "Lesbianas"},
				Broader: []string{ns + "796500"}},
			{ID: "https://homosaurus.org/v4/homoit0000506", Labels: map[string]string{"en": "Out of namespace"}},
		}},
		// A second work repeating a term with a label the first lacked: the
		// language sets merge, first non-empty per language wins.
		{ID: "w2", Subjects: []project.Subject{
			{ID: ns + "1136767", Labels: map[string]string{"en": "IGNORED duplicate", "fr": "Toxicomanie"}},
		}},
	}
	out, terms := subsetFromCatalog("fast", ns, works)
	s := string(out)
	if terms != 2 {
		t.Fatalf("terms = %d, want 2 (in-namespace labeled concepts)", terms)
	}
	if !strings.Contains(s, "<authority:fast>") {
		t.Fatalf("output not graphed authority:fast:\n%s", s)
	}
	for _, want := range []string{`"Substance abuse"@en`, `"Toxicomanie"@fr`, `"Lesbians"@en`, `"Lesbianas"@es`} {
		if !strings.Contains(s, want) {
			t.Fatalf("missing %s:\n%s", want, s)
		}
	}
	if !strings.Contains(s, `<http://www.w3.org/2004/02/skos/core#broader> <`+ns+`796500>`) {
		t.Fatalf("broader dropped:\n%s", s)
	}
	if strings.Contains(s, "IGNORED") {
		t.Fatalf("later duplicate label overwrote the first:\n%s", s)
	}
	if strings.Contains(s, "homosaurus") {
		t.Fatalf("out-of-namespace subject kept:\n%s", s)
	}
	// Deterministic: same input, same bytes.
	again, _ := subsetFromCatalog("fast", ns, works)
	if string(again) != s {
		t.Fatal("output not deterministic")
	}
}
