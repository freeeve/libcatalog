// catalog.nq has more than one writer. BuildWorks and BuildCorpus each
// write it during ingest; SerializeGrains rewrites it from the committed grains.
// The ingest writers re-encoded the graphs through one rdf.Encoder and emitted
// traversal-order `_:b1, _:b2, …` labels, so a build that ran ingest without
// serialize published the unstable dump had just fixed. The file meant
// two different things depending on which writer ran last.
//
// It means one thing now, and these tests are what hold it there.
package bibframe

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/freeeve/libcat/storage"
	codex "github.com/freeeve/libcodex"
	codexbf "github.com/freeeve/libcodex/bibframe"
)

// catalogOf reads the bulk file a builder wrote into dir.
func catalogOf(t *testing.T, dir string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "catalog.nq"))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// reserialized reruns SerializeGrains over dir's grains into a scratch sink and
// returns the catalog.nq it produces, leaving dir's own untouched.
func reserialized(t *testing.T, dir string) []byte {
	t.Helper()
	out := t.TempDir()
	if _, err := SerializeGrains(dir, storage.Dir(out)); err != nil {
		t.Fatalf("SerializeGrains: %v", err)
	}
	return catalogOf(t, out)
}

var traversalLabel = regexp.MustCompile(`(^|\s)_:b\d+(\s|\.)`)

// assertMergedForm is the contract every writer of catalog.nq owes: no
// traversal-counter labels, and byte-equality with the merge of the grains on
// disk. The second implies the first, but the first names the symptom the
// reporter saw.
func assertMergedForm(t *testing.T, dir string) {
	t.Helper()
	got := catalogOf(t, dir)
	if len(got) == 0 {
		t.Fatal("empty catalog.nq; the fixture stopped exercising anything")
	}
	if m := traversalLabel.FindIndex(got); m != nil {
		t.Errorf("catalog.nq carries traversal-counter labels: %q", got[m[0]:min(m[1]+40, len(got))])
	}
	if want := reserialized(t, dir); !bytes.Equal(got, want) {
		t.Errorf("the builder's catalog.nq is not the merge of its own grains (%d vs %d bytes)\n got: %s\nwant: %s",
			len(got), len(want), head(got), head(want))
	}
}

func head(b []byte) []byte {
	if len(b) > 200 {
		return b[:200]
	}
	return b
}

func twoWorkGroups() []WorkGroup {
	return []WorkGroup{
		{
			WorkID:    "wone",
			Work:      codexbf.Work{Class: "Text", Titles: []codexbf.Title{{MainTitle: "One"}}},
			Instances: []GroupInstance{{InstanceID: "ione", Instance: codexbf.Instance{Identifiers: []codexbf.Identifier{{Class: "Isbn", Value: "9780000000001"}}}}},
		},
		{
			WorkID:    "wtwo",
			Work:      codexbf.Work{Class: "Text", Titles: []codexbf.Title{{MainTitle: "Two"}}},
			Instances: []GroupInstance{{InstanceID: "itwo", Instance: codexbf.Instance{Identifiers: []codexbf.Identifier{{Class: "Isbn", Value: "9780000000002"}}}}},
		},
	}
}

// The exact failure the reporter hit: `lcat build --only ingest`, then export.
func TestBuildWorksCatalogIsTheMergeOfItsGrains(t *testing.T) {
	dir := t.TempDir()
	if _, err := BuildWorks(storage.Dir(dir), twoWorkGroups(), "overdrive"); err != nil {
		t.Fatalf("BuildWorks: %v", err)
	}
	assertMergedForm(t, dir)
}

func TestBuildCorpusCatalogIsTheMergeOfItsGrains(t *testing.T) {
	dir := t.TempDir()
	recs := []*codex.Record{
		makeRecord("001", "One", "Alpha, A."),
		makeRecord("002", "Two", "Beta, B."),
	}
	if _, err := BuildCorpus(storage.Dir(dir), recs, "marc"); err != nil {
		t.Fatalf("BuildCorpus: %v", err)
	}
	assertMergedForm(t, dir)
}

// Editorial statements are preserved across re-ingest and live in the grain, so
// they must reach the bulk file through the grain like everything else. The old
// BuildWorks appended them separately, after the re-encoded feed lines.
func TestBuildWorksCarriesEditorialIntoTheMerge(t *testing.T) {
	dir := t.TempDir()
	works := twoWorkGroups()
	works[0].Editorial = []byte("<https://ex.org/wone> <https://ex.org/note> \"kept\" <editorial:> .\n")
	if _, err := BuildWorks(storage.Dir(dir), works, "overdrive"); err != nil {
		t.Fatalf("BuildWorks: %v", err)
	}
	if got := catalogOf(t, dir); !bytes.Contains(got, []byte(`"kept"`)) {
		t.Fatal("editorial statement missing from catalog.nq")
	}
	assertMergedForm(t, dir)
}

// Running serialize after ingest must be a no-op on a single-provider corpus.
// It was not: it rewrote every blank label, so the published sha256 depended on
// whether the pipeline had happened to run the step.
func TestSerializeAfterIngestChangesNothing(t *testing.T) {
	dir := t.TempDir()
	sink := storage.Dir(dir)
	if _, err := BuildWorks(sink, twoWorkGroups(), "overdrive"); err != nil {
		t.Fatalf("BuildWorks: %v", err)
	}
	before := catalogOf(t, dir)
	if _, err := SerializeGrains(dir, sink); err != nil {
		t.Fatalf("SerializeGrains: %v", err)
	}
	if after := catalogOf(t, dir); !bytes.Equal(before, after) {
		t.Errorf("serialize rewrote ingest's catalog.nq (%d -> %d bytes)", len(before), len(after))
	}
}
