package bibframe

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/freeeve/libcodex/rdf"

	"github.com/freeeve/libcat/storage/blob"
)

// sampleGrain builds a small grain with feed statements for one Work.
func sampleGrain(t *testing.T) []byte {
	t.Helper()
	ds := &rdf.Dataset{}
	feed := FeedGraph("overdrive")
	work := rdf.NewIRI(WorkIRI("w1"))
	ds.Add(work, rdf.NewIRI("http://id.loc.gov/ontologies/bibframe/title"), rdf.NewLiteral("A Book", "", ""), feed)
	ds.Add(rdf.NewIRI(InstanceIRI("i1")), rdf.NewIRI("http://id.loc.gov/ontologies/bibframe/instanceOf"), work, feed)
	nq, err := ds.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	return nq
}

func quad(s, p, o string) rdf.Quad {
	return rdf.Quad{S: rdf.NewIRI(s), P: rdf.NewIRI(p), O: rdf.NewIRI(o)}
}

func TestApplyEditorialPatch(t *testing.T) {
	grain := sampleGrain(t)
	subjectQuad := quad(WorkIRI("w1"), bfSubjectIRI, "https://homosaurus.org/v4/homoit0001235")

	out, err := ApplyEditorialPatch(grain, Patch{Add: []rdf.Quad{subjectQuad}})
	if err != nil {
		t.Fatalf("ApplyEditorialPatch: %v", err)
	}
	if !strings.Contains(string(out), "<editorial:>") {
		t.Fatalf("editorial graph missing:\n%s", out)
	}
	if !strings.Contains(string(out), "homoit0001235") {
		t.Fatalf("subject missing:\n%s", out)
	}
	// Idempotent: applying again is byte-identical.
	again, err := ApplyEditorialPatch(out, Patch{Add: []rdf.Quad{subjectQuad}})
	if err != nil || !bytes.Equal(out, again) {
		t.Fatalf("not idempotent (err %v)", err)
	}
	// Remove restores the original bytes exactly (canonicalization is
	// deterministic).
	removed, err := ApplyEditorialPatch(again, Patch{Remove: []rdf.Quad{subjectQuad}})
	if err != nil || !bytes.Equal(removed, grain) {
		t.Fatalf("remove did not restore original (err %v)\nwant:\n%s\ngot:\n%s", err, grain, removed)
	}
	// Removing from editorial never touches feed statements.
	feedTitle := rdf.Quad{S: rdf.NewIRI(WorkIRI("w1")), P: rdf.NewIRI("http://id.loc.gov/ontologies/bibframe/title"), O: rdf.NewLiteral("A Book", "", "")}
	unchanged, err := ApplyEditorialPatch(grain, Patch{Remove: []rdf.Quad{feedTitle}})
	if err != nil || !bytes.Equal(unchanged, grain) {
		t.Fatal("editorial remove reached the feed graph")
	}
}

func TestPatchRejectsBlankNodes(t *testing.T) {
	grain := sampleGrain(t)
	blank := rdf.Quad{S: rdf.NewBlank("b0"), P: rdf.NewIRI(bfSubjectIRI), O: rdf.NewIRI("https://x")}
	if _, err := ApplyEditorialPatch(grain, Patch{Add: []rdf.Quad{blank}}); !errors.Is(err, ErrBlankNode) {
		t.Fatalf("blank subject: %v", err)
	}
	blankObj := rdf.Quad{S: rdf.NewIRI(WorkIRI("w1")), P: rdf.NewIRI(bfSubjectIRI), O: rdf.NewBlank("b1")}
	if _, err := ApplyEditorialPatch(grain, Patch{Remove: []rdf.Quad{blankObj}}); !errors.Is(err, ErrBlankNode) {
		t.Fatalf("blank object: %v", err)
	}
}

func TestReplaceGraph(t *testing.T) {
	grain := sampleGrain(t)
	enrich := EnrichmentGraph("locsh")
	first := []rdf.Quad{quad(WorkIRI("w1"), bfSubjectIRI, "http://id.loc.gov/authorities/subjects/sh1")}
	out, err := ReplaceGraph(grain, enrich, first)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "<enrichment:locsh>") || !strings.Contains(string(out), "sh1") {
		t.Fatalf("enrichment graph missing:\n%s", out)
	}
	// Re-enrichment with a different set drops the old statement.
	second := []rdf.Quad{quad(WorkIRI("w1"), bfSubjectIRI, "http://id.loc.gov/authorities/subjects/sh2")}
	out2, err := ReplaceGraph(out, enrich, second)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out2), "subjects/sh1") || !strings.Contains(string(out2), "subjects/sh2") {
		t.Fatalf("replace did not replace:\n%s", out2)
	}
	// Idempotent re-run: byte-identical.
	out3, err := ReplaceGraph(out2, enrich, second)
	if err != nil || !bytes.Equal(out2, out3) {
		t.Fatal("re-enrichment not idempotent")
	}
	// Feed graph untouched throughout.
	if !strings.Contains(string(out2), "A Book") {
		t.Fatal("feed statements lost")
	}
	// Replacing with nothing removes the graph entirely.
	cleared, err := ReplaceGraph(out2, enrich, nil)
	if err != nil || strings.Contains(string(cleared), "enrichment:locsh") {
		t.Fatalf("clear failed (err %v)", err)
	}
	if !bytes.Equal(cleared, grain) {
		t.Fatal("clearing did not restore the original grain")
	}
}

func TestAppendAuthoritySubject(t *testing.T) {
	grain := sampleGrain(t)
	subj := AuthoritySubject{
		URI:     "https://homosaurus.org/v4/homoit0001235",
		Labels:  map[string]string{"en": "Transgender people", "es": "Personas transgénero"},
		Broader: []string{"https://homosaurus.org/v4/homoit0000508"},
	}
	out, err := AppendAuthoritySubject(grain, "w1", subj, "homosaurus")
	if err != nil {
		t.Fatal(err)
	}
	text := string(out)
	// Subject link in editorial:, term description in authority:homosaurus.
	if !strings.Contains(text, "<editorial:>") || !strings.Contains(text, "<authority:homosaurus>") {
		t.Fatalf("graphs missing:\n%s", text)
	}
	for _, want := range []string{"Transgender people", "Personas transg", "homoit0000508", skosPrefLabelIRI, skosBroaderIRI} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q:\n%s", want, text)
		}
	}
	// Idempotent.
	again, err := AppendAuthoritySubject(out, "w1", subj, "homosaurus")
	if err != nil || !bytes.Equal(out, again) {
		t.Fatal("not idempotent")
	}
	// The label quads route to the authority graph, not editorial.
	for line := range strings.SplitSeq(text, "\n") {
		if strings.Contains(line, skosPrefLabelIRI) && !strings.Contains(line, "<authority:homosaurus>") {
			t.Fatalf("label in wrong graph: %s", line)
		}
	}
}

// TestReingestPreservesNonFeedGraphs proves the invariant the whole editorial
// model rests on: preservedQuads keeps editorial:, authority:<vocab>, and
// enrichment:<name> graphs when the feed graph is rewritten.
func TestReingestPreservesNonFeedGraphs(t *testing.T) {
	grain := sampleGrain(t)
	var err error
	grain, err = ApplyEditorialPatch(grain, Patch{Add: []rdf.Quad{quad(WorkIRI("w1"), bfSubjectIRI, "https://homosaurus.org/v4/x")}})
	if err != nil {
		t.Fatal(err)
	}
	grain, err = ReplaceGraph(grain, EnrichmentGraph("locsh"), []rdf.Quad{quad(WorkIRI("w1"), bfSubjectIRI, "http://id.loc.gov/authorities/subjects/sh9")})
	if err != nil {
		t.Fatal(err)
	}
	grain, err = ApplyPatch(grain, AuthorityGraph("homosaurus"), Patch{Add: []rdf.Quad{
		{S: rdf.NewIRI("https://homosaurus.org/v4/x"), P: rdf.NewIRI(skosPrefLabelIRI), O: rdf.NewLiteral("X", "en", "")},
	}})
	if err != nil {
		t.Fatal(err)
	}
	ds, err := rdf.ParseNQuads(grain)
	if err != nil {
		t.Fatal(err)
	}
	text := string(preservedQuads(ds, FeedGraph("overdrive")))
	for _, want := range []string{"<editorial:>", "<enrichment:locsh>", "<authority:homosaurus>"} {
		if !strings.Contains(text, want) {
			t.Fatalf("preserved missing %s:\n%s", want, text)
		}
	}
	if strings.Contains(text, "A Book") {
		t.Fatal("feed statements leaked into preserved set")
	}
}

// TestLoadPriorStoreParity proves the blob.Store walk recovers the same
// Prior as the filesystem walk over an identical tree, plus usable etags.
func TestLoadPriorStoreParity(t *testing.T) {
	grain := sampleGrain(t)
	grain, err := AddMergeMarker(grain, "w9", "w1")
	if err != nil {
		t.Fatal(err)
	}
	grain, err = AddSplitMarkers(grain, "w2", "w1", []string{"i1"})
	if err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "aa"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "aa", "w1.nq"), grain, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "catalog.nq"), []byte{}, 0o644); err != nil {
		t.Fatal(err)
	}
	fsPrior, err := LoadPrior(dir, "overdrive")
	if err != nil {
		t.Fatal(err)
	}

	st := blob.NewMem()
	if _, err := st.Put(t.Context(), "data/works/aa/w1.nq", grain, blob.PutOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Put(t.Context(), "data/works/catalog.nq", []byte{}, blob.PutOptions{}); err != nil {
		t.Fatal(err)
	}
	storePrior, etags, err := LoadPriorStore(t.Context(), st, "data/works/", "overdrive")
	if err != nil {
		t.Fatal(err)
	}

	if len(storePrior.Grains) != len(fsPrior.Grains) || len(storePrior.Grains) != 1 {
		t.Fatalf("grains: store %d, fs %d", len(storePrior.Grains), len(fsPrior.Grains))
	}
	if len(storePrior.Merges) != 1 || storePrior.Merges[0] != fsPrior.Merges[0] {
		t.Fatalf("merges: %+v vs %+v", storePrior.Merges, fsPrior.Merges)
	}
	if len(storePrior.Pins) != 1 || storePrior.Pins[0] != fsPrior.Pins[0] {
		t.Fatalf("pins: %+v vs %+v", storePrior.Pins, fsPrior.Pins)
	}
	if !bytes.Equal(storePrior.Editorial["w1"], fsPrior.Editorial["w1"]) {
		t.Fatalf("editorial mismatch:\n%s\nvs\n%s", storePrior.Editorial["w1"], fsPrior.Editorial["w1"])
	}
	etag, ok := etags["data/works/aa/w1.nq"]
	if !ok || etag == "" {
		t.Fatalf("etags = %v", etags)
	}
	if _, ok := etags["data/works/catalog.nq"]; ok {
		t.Fatal("catalog.nq should be skipped")
	}
	// The etag is live: a conditional write with it succeeds.
	if _, err := st.Put(t.Context(), "data/works/aa/w1.nq", grain, blob.PutOptions{IfMatch: etag}); err != nil {
		t.Fatalf("conditional write with recovered etag: %v", err)
	}
}
