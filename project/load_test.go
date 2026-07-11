package project

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/freeeve/libcodex/rdf"
)

const (
	lcsh   = "http://id.loc.gov/authorities/subjects/"
	skos   = "http://www.w3.org/2004/02/skos/core#"
	bfIRI  = "http://id.loc.gov/ontologies/bibframe/"
	rdfNS_ = "http://www.w3.org/1999/02/22-rdf-syntax-ns#"
)

// authorityCorpus is a work grain plus an LCSH-shaped authority graph carrying
// the four kinds of quad a real snapshot has: the prefLabel and broader the
// projection reads, and the altLabel/narrower/related/type it does not.
//
// Three terms, and only one is referenced by the Work:
//
//	sh-child   <- the Work's subject
//	sh-parent <- reached only through skos:broader (the case)
//	sh-unused  <- referenced by nothing, like the other 450,000 in LCSH
func authorityCorpus(t *testing.T) string {
	t.Helper()
	nq := `<#waaWork> <` + rdfNS_ + `type> <` + bfIRI + `Work> <feed:marc> .
<#waaWork> <` + bfIRI + `title> _:t <feed:marc> .
_:t <` + bfIRI + `mainTitle> "A Book" <feed:marc> .
<#waaWork> <` + bfIRI + `subject> <` + lcsh + `sh-child> <feed:marc> .
<` + lcsh + `sh-child> <` + skos + `prefLabel> "Child Heading"@en <authority:lcsh> .
<` + lcsh + `sh-child> <` + skos + `broader> <` + lcsh + `sh-parent> <authority:lcsh> .
<` + lcsh + `sh-child> <` + skos + `altLabel> "Alternative Heading"@en <authority:lcsh> .
<` + lcsh + `sh-child> <` + skos + `related> <` + lcsh + `sh-unused> <authority:lcsh> .
<` + lcsh + `sh-child> <` + rdfNS_ + `type> <` + skos + `Concept> <authority:lcsh> .
<` + lcsh + `sh-parent> <` + skos + `prefLabel> "Parent Heading"@en <authority:lcsh> .
<` + lcsh + `sh-parent> <` + skos + `narrower> <` + lcsh + `sh-child> <authority:lcsh> .
<` + lcsh + `sh-unused> <` + skos + `prefLabel> "Unused Heading"@en <authority:lcsh> .
<` + lcsh + `sh-unused> <` + skos + `broader> <` + lcsh + `sh-parent> <authority:lcsh> .
`
	path := filepath.Join(t.TempDir(), "catalog.nq")
	if err := os.WriteFile(path, []byte(nq), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// quadStrings renders a dataset for set comparison.
func quadStrings(ds *rdf.Dataset) []string {
	out := make([]string, 0, len(ds.Quads))
	for _, q := range ds.Quads {
		out = append(out, q.S.Value+" "+q.P.Value+" "+q.O.Value+" "+q.G.Value)
	}
	return out
}

func has(qs []string, substr string) bool {
	for _, q := range qs {
		if strings.Contains(q, substr) {
			return true
		}
	}
	return false
}

// . The whole point: a projection over the filtered dataset is the same
// projection. Everything below explains *why* it can be, but this is the promise.
func TestFilteredLoadProjectsIdenticallyToTheWholeCorpus(t *testing.T) {
	path := authorityCorpus(t)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	whole, err := Project(raw, "marc")
	if err != nil {
		t.Fatal(err)
	}
	ds, err := LoadDataset(path)
	if err != nil {
		t.Fatal(err)
	}
	filtered := ProjectDataset(ds, "marc")

	if !reflect.DeepEqual(whole, filtered) {
		t.Fatalf("filtered projection differs\n whole    = %+v\n filtered = %+v", whole, filtered)
	}
	// And the fixture must actually exercise the vocabulary, or "identical" only
	// means "identically empty".
	if len(whole.Terms) != 2 {
		t.Fatalf("fixture projects %d terms, want 2 (child + its broader parent)", len(whole.Terms))
	}
	if len(whole.Works) != 1 {
		t.Fatalf("fixture projects %d works, want 1", len(whole.Works))
	}
}

// The predicates no index reads are 45% of a real corpus. They are dropped, and
// the two that are read survive -- an absence is not evidence of a filter.
func TestLoadDatasetDropsTheAuthorityPredicatesNothingReads(t *testing.T) {
	ds, err := LoadDataset(authorityCorpus(t))
	if err != nil {
		t.Fatal(err)
	}
	qs := quadStrings(ds)

	for _, pred := range []string{skos + "altLabel", skos + "narrower", skos + "related", rdfNS_ + "type " + skos + "Concept"} {
		if has(qs, pred) {
			t.Errorf("authority quad with unread predicate %q was retained", pred)
		}
	}
	if !has(qs, skos+"prefLabel") {
		t.Error("prefLabel was dropped; the filter drops everything")
	}
	if !has(qs, skos+"broader") {
		t.Error("broader was dropped; the filter drops everything")
	}
}

// An ancestor no Work names still has to carry its label: the browse artifact
// unions subtree postings into it, and without a label it mints it label-less
// . This is the closure the seed alone would miss.
func TestLoadDatasetKeepsBroaderAncestorsNoWorkReferences(t *testing.T) {
	ds, err := LoadDataset(authorityCorpus(t))
	if err != nil {
		t.Fatal(err)
	}
	qs := quadStrings(ds)
	if !has(qs, lcsh+"sh-parent "+skos+"prefLabel") {
		t.Error("the broader ancestor's label was dropped; its term page has no heading")
	}
}

// The 450,000 headings nobody references are the reason this exists.
func TestLoadDatasetDropsTermsNoWorkReaches(t *testing.T) {
	ds, err := LoadDataset(authorityCorpus(t))
	if err != nil {
		t.Fatal(err)
	}
	qs := quadStrings(ds)
	// sh-unused is broader-linked to sh-parent, but nothing reaches *it*: the
	// closure walks upward, not down. A filter that walked skos:narrower, or that
	// kept any term mentioned anywhere, would drag in the whole vocabulary.
	if has(qs, lcsh+"sh-unused "+skos+"prefLabel") {
		t.Error("an unreferenced heading was retained")
	}
	// Control: the referenced one is here.
	if !has(qs, lcsh+"sh-child "+skos+"prefLabel") {
		t.Error("the referenced heading was dropped")
	}
}

// Work, editorial and alias graphs pass through untouched, whatever their
// predicates. Only the authority graphs are filtered by predicate.
func TestLoadDatasetKeepsNonAuthorityGraphsWhole(t *testing.T) {
	ds, err := LoadDataset(authorityCorpus(t))
	if err != nil {
		t.Fatal(err)
	}
	feed := 0
	for _, q := range ds.Quads {
		if q.G.Value == "feed:marc" {
			feed++
		}
	}
	if feed != 4 {
		t.Errorf("kept %d feed quads, want all 4", feed)
	}
}

// A truncated catalog.nq is an error, not a smaller catalog (libcodex
// filed from here; strict since libcodex v0.26.0).
//
// This inverts the test that used to live here, which pinned the old silent-skip
// so it would fail loudly the day libcodex changed. It did. What remains is the
// property that mattered all along: the build refuses a short read of its own
// input rather than projecting a catalog missing whatever came after the bad byte
// and exiting 0 -- the failure class refuses everywhere else.
func TestATruncatedCatalogIsRefused(t *testing.T) {
	path := filepath.Join(t.TempDir(), "catalog.nq")
	good := `<#waaWork> <` + rdfNS_ + `type> <` + bfIRI + `Work> <feed:marc> .` + "\n"
	raw := []byte(good + "<#broken \n")
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := LoadDataset(path)
	if err == nil {
		t.Fatal("LoadDataset accepted a truncated catalog and returned a smaller one")
	}
	var se *rdf.SyntaxError
	if !errors.As(err, &se) {
		t.Fatalf("error is not a *rdf.SyntaxError, so nothing can say where: %v", err)
	}
	if se.Line != 2 {
		t.Errorf("SyntaxError.Line = %d, want 2 (the bad line, 1-based)", se.Line)
	}
	if !strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), "truncated or corrupt") {
		t.Errorf("the error does not name the file and what is wrong with it: %v", err)
	}

	// The bulk parser agrees, so a caller that has not moved to LoadDataset is not
	// quietly left on the old contract.
	if _, err := rdf.ParseNQuadsShared(raw); err == nil {
		t.Error("ParseNQuadsShared still accepts the bad line")
	}

	// Control: the same file without the bad line loads, so the refusal above is the
	// malformed line and not the fixture.
	if err := os.WriteFile(path, []byte(good), 0o644); err != nil {
		t.Fatal(err)
	}
	ds, err := LoadDataset(path)
	if err != nil {
		t.Fatalf("a well-formed catalog was refused: %v", err)
	}
	if len(ds.Quads) != 1 {
		t.Fatalf("kept %d quads, want 1", len(ds.Quads))
	}
}

func TestLoadDatasetReportsAMissingFile(t *testing.T) {
	if _, err := LoadDataset(filepath.Join(t.TempDir(), "nope.nq")); err == nil {
		t.Fatal("a missing catalog loaded without error")
	}
}

// BenchmarkLoadDataset and BenchmarkProjectDataset guard the two costs
// measured: the load's memory (report allocs) and the per-feed projection, which
// used to reparse the whole corpus. Run against a real corpus with
// LCAT_BENCH_NQ=/path/to/catalog.nq; skipped otherwise, since a synthetic corpus
// large enough to be meaningful would dominate the suite's runtime.
func BenchmarkLoadDataset(b *testing.B) {
	path := os.Getenv("LCAT_BENCH_NQ")
	if path == "" {
		b.Skip("set LCAT_BENCH_NQ to a catalog.nq")
	}
	b.ReportAllocs()
	for b.Loop() {
		if _, err := LoadDataset(path); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkProjectDataset(b *testing.B) {
	path := os.Getenv("LCAT_BENCH_NQ")
	if path == "" {
		b.Skip("set LCAT_BENCH_NQ to a catalog.nq")
	}
	ds, err := LoadDataset(path)
	if err != nil {
		b.Fatal(err)
	}
	provider := os.Getenv("LCAT_BENCH_PROVIDER")
	if provider == "" {
		provider = "marc"
	}
	b.ReportAllocs()
	for b.Loop() {
		ProjectDataset(ds, provider)
	}
}
