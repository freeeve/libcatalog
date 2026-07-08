package marcview_test

import (
	"bytes"
	"os"
	"strings"
	"testing"

	codexbf "github.com/freeeve/libcodex/bibframe"

	"github.com/freeeve/libcat/bibframe"
	"github.com/freeeve/libcat/storage"

	"github.com/freeeve/libcat/backend/marcview"
)

// grainFixture builds a grain exactly the way MARC ingest does from the
// vendored MARC Express sample: crosswalked BIBFRAME plus the known-loss
// fields as the verbatim sidecar.
func grainFixture(t *testing.T) []byte {
	t.Helper()
	f, err := os.Open("../../ingest/overdrive/testdata/marc-express/od-sample-ebook.mrc")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	recs, err := bibframe.ReadMARC(f)
	if err != nil || len(recs) == 0 {
		t.Fatalf("read sample: %v", err)
	}
	rec := recs[0]
	bib := codexbf.FromRecord(rec)
	wg := bibframe.WorkGroup{
		WorkID: "wmarc00000001",
		Work:   bib.Work,
		Instances: []bibframe.GroupInstance{{
			InstanceID: "imarc00000001",
			Instance:   bib.Instance,
			Verbatim:   bibframe.VerbatimFields(rec),
		}},
	}
	dir := t.TempDir()
	if _, err := bibframe.BuildWorks(storage.Dir(dir), []bibframe.WorkGroup{wg}, "marc"); err != nil {
		t.Fatal(err)
	}
	grain, err := os.ReadFile(dir + "/" + bibframe.GrainPath(wg.WorkID))
	if err != nil {
		t.Fatal(err)
	}
	return grain
}

func TestViewAnnotatesAndLists(t *testing.T) {
	grain := grainFixture(t)
	docs, err := marcview.View(grain)
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != 1 {
		t.Fatalf("docs = %d", len(docs))
	}
	doc := docs[0]
	if doc.Node != "#imarc00000001Instance" || len(doc.Leader) != 24 {
		t.Fatalf("doc head = %+v", doc.Node)
	}
	var have037, have245 bool
	for _, f := range doc.Fields {
		if f.Tag == "037" {
			have037 = true
			if f.Lossy == "" {
				t.Error("037 not annotated lossy")
			}
		}
		if f.Tag == "245" {
			have245 = true
			if f.Lossy != "" {
				t.Error("245 wrongly annotated lossy")
			}
		}
	}
	if !have037 || !have245 {
		t.Fatalf("missing fields: 037=%v 245=%v", have037, have245)
	}
}

func TestUntouchedSaveIsNoOp(t *testing.T) {
	grain := grainFixture(t)
	docs, err := marcview.View(grain)
	if err != nil {
		t.Fatal(err)
	}
	updated, err := marcview.Save(grain, 0, docs[0])
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(updated, grain) {
		t.Fatal("untouched save changed the grain")
	}
}

func TestSingleFieldEditLandsAlone(t *testing.T) {
	grain := grainFixture(t)
	docs, _ := marcview.View(grain)
	edited := docs[0]
	var oldSummary string
	for i, f := range edited.Fields {
		if f.Tag == "520" {
			oldSummary = f.Subfields[0].Value
			edited.Fields[i].Subfields = []marcview.Subfield{{Code: "a", Value: "An edited summary."}}
			break
		}
	}
	if oldSummary == "" {
		t.Skip("sample has no 520")
	}
	updated, err := marcview.Save(grain, 0, edited)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(updated, grain) {
		t.Fatal("edit produced no change")
	}
	// Only the summary group landed editorially: every editorial line
	// mentions the summary predicate or the override marker for it.
	for line := range strings.SplitSeq(string(updated), "\n") {
		if !strings.Contains(line, "<editorial:>") {
			continue
		}
		if !strings.Contains(line, "summary") && !strings.Contains(line, "overrides") {
			t.Errorf("unexpected editorial statement: %s", line)
		}
	}
	// The view now shows exactly the edited value, once.
	docs2, err := marcview.View(updated)
	if err != nil {
		t.Fatal(err)
	}
	var summaries []string
	for _, f := range docs2[0].Fields {
		if f.Tag == "520" {
			summaries = append(summaries, f.Subfields[0].Value)
		}
	}
	if len(summaries) != 1 || summaries[0] != "An edited summary." {
		t.Fatalf("summaries after edit = %v", summaries)
	}
	// The feed statement survives untouched in its graph (revertability).
	if !strings.Contains(string(updated), oldSummary[:20]) {
		t.Error("feed summary was rewritten, not shadowed")
	}
	// Saving the same doc again is a no-op (deterministic skolems).
	again, err := marcview.Save(updated, 0, docs2[0])
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(again, updated) {
		t.Fatal("re-save of an identical doc changed the grain")
	}
}

func TestLossyEditRoundTripsViaVerbatim(t *testing.T) {
	grain := grainFixture(t)
	docs, _ := marcview.View(grain)
	edited := docs[0]
	found := false
	for i, f := range edited.Fields {
		if f.Tag == "037" && len(f.Subfields) > 0 {
			found = true
			edited.Fields[i].Subfields[0].Value = "EDITED-RESERVE-ID"
			break
		}
	}
	if !found {
		t.Skip("sample has no 037")
	}
	updated, err := marcview.Save(grain, 0, edited)
	if err != nil {
		t.Fatal(err)
	}
	docs2, err := marcview.View(updated)
	if err != nil {
		t.Fatal(err)
	}
	var values []string
	for _, f := range docs2[0].Fields {
		if f.Tag == "037" && len(f.Subfields) > 0 {
			values = append(values, f.Subfields[0].Value)
		}
	}
	if len(values) != 1 || values[0] != "EDITED-RESERVE-ID" {
		t.Fatalf("037 after edit = %v", values)
	}
	// The MARC export path reproduces the edited field.
	recs, err := bibframe.DecodeGrainMARC(updated)
	if err != nil {
		t.Fatal(err)
	}
	if got := recs[0].SubfieldValue("037", 'a'); got != "EDITED-RESERVE-ID" {
		t.Fatalf("export 037$a = %q", got)
	}
}
