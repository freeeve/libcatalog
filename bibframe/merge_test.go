package bibframe

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/freeeve/libcat/identity"
	"github.com/freeeve/libcat/storage"
	codexbf "github.com/freeeve/libcodex/bibframe"
)

// mergeMarker is the editorial lcat:mergedInto statement recording that wret was
// merged into wsurv.
const mergeMarker = "<#wretWork> <" + PredMergedInto + "> <#wsurvWork> <editorial:> .\n"

func TestScanMerges(t *testing.T) {
	grain := []byte("<#wsurvWork> <http://id.loc.gov/ontologies/bibframe/mainTitle> \"x\" <feed:overdrive> .\n" + mergeMarker)
	got, err := ScanMerges(grain)
	if err != nil {
		t.Fatalf("ScanMerges: %v", err)
	}
	want := []identity.Merge{{From: "wret", To: "wsurv"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ScanMerges = %v, want %v", got, want)
	}
}

func TestAddMergeMarkerIdempotent(t *testing.T) {
	base := []byte("<#wsurvWork> <http://id.loc.gov/ontologies/bibframe/mainTitle> \"x\" <feed:overdrive> .\n")
	once, err := AddMergeMarker(base, "wret", "wsurv")
	if err != nil {
		t.Fatalf("AddMergeMarker: %v", err)
	}
	if !bytes.Contains(once, []byte(PredMergedInto)) {
		t.Fatal("marker not added")
	}
	twice, err := AddMergeMarker(once, "wret", "wsurv")
	if err != nil {
		t.Fatalf("AddMergeMarker (again): %v", err)
	}
	if !bytes.Equal(once, twice) {
		t.Error("AddMergeMarker is not idempotent")
	}
	merges, _ := ScanMerges(twice)
	if len(merges) != 1 {
		t.Errorf("expected exactly one merge after re-adding, got %d", len(merges))
	}
}

func TestRetiredWorks(t *testing.T) {
	got := RetiredWorks([]identity.Merge{{From: "b", To: "z"}, {From: "a", To: "z"}, {From: "a", To: "z"}, {From: "x", To: "x"}})
	want := []string{"a", "b"} // sorted, distinct, self-merge dropped
	if !reflect.DeepEqual(got, want) {
		t.Errorf("RetiredWorks = %v, want %v", got, want)
	}
}

// TestMergeReingest is the end-to-end gate: a recorded merge moves the
// retired Work's Instance onto the survivor, retires the old grain, keeps the
// decision durable, and re-ingests byte-identically.
func TestMergeReingest(t *testing.T) {
	dir := t.TempDir()
	sink := storage.Dir(dir)

	survivor := WorkGroup{
		WorkID: "wsurv",
		Work:   codexbf.Work{Class: "Text", Titles: []codexbf.Title{{MainTitle: "Le Petit Prince"}}},
		Instances: []GroupInstance{{
			InstanceID: "isurv",
			Instance:   codexbf.Instance{Identifiers: []codexbf.Identifier{{Class: "Isbn", Value: "9780000000111"}}},
		}},
	}
	retired := WorkGroup{
		WorkID: "wret",
		Work:   codexbf.Work{Class: "Text", Titles: []codexbf.Title{{MainTitle: "The Little Prince"}}},
		Instances: []GroupInstance{{
			InstanceID: "iret",
			Instance:   codexbf.Instance{Identifiers: []codexbf.Identifier{{Class: "Isbn", Value: "9780000000222"}}},
		}},
	}
	if _, err := BuildWorks(sink, []WorkGroup{survivor, retired}, "overdrive"); err != nil {
		t.Fatalf("initial build: %v", err)
	}

	// Record the merge in the survivor's grain (what `lcat merge` does).
	survPath := filepath.Join(dir, filepath.FromSlash(GrainPath("wsurv")))
	marked, err := AddMergeMarker(readFile(t, survPath), "wret", "wsurv")
	if err != nil {
		t.Fatalf("AddMergeMarker: %v", err)
	}
	if err := os.WriteFile(survPath, marked, 0o644); err != nil {
		t.Fatal(err)
	}

	// Re-ingest: the merge must be recovered, both Instances resolve onto wsurv, and
	// the retired grain is dropped.
	survPath = reingestMerge(t, dir, sink)

	got := readFile(t, survPath)
	for _, want := range []string{"9780000000111", "9780000000222", PredMergedInto} {
		if !bytes.Contains(got, []byte(want)) {
			t.Errorf("merged grain missing %q", want)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(GrainPath("wret")))); !os.IsNotExist(err) {
		t.Error("retired grain wret was not removed")
	}

	// A second re-ingest is byte-identical (determinism gate).
	before := readFile(t, survPath)
	reingestMerge(t, dir, sink)
	if after := readFile(t, survPath); !bytes.Equal(before, after) {
		t.Error("merge re-ingest is not byte-stable")
	}
}

// reingestMerge replays one ingest over the grains under dir: recover prior
// identity + merges, resolve the two fixture records, regroup onto the survivor,
// rebuild, and drop retired grains. It returns the survivor grain path.
func reingestMerge(t *testing.T, dir string, sink storage.Sink) string {
	t.Helper()
	prior, err := LoadPrior(dir, "overdrive")
	if err != nil {
		t.Fatalf("load prior: %v", err)
	}
	r := identity.NewResolver()
	identity.SeedResolver(r, prior.Grains)
	for _, m := range prior.Merges {
		r.SeedMerge(m.From, m.To)
	}

	recs := []identity.Record{
		{ProviderKeys: []string{identity.ProviderKey(identity.SchemeISBN, "9780000000111")}, Title: "Le Petit Prince"},
		{ProviderKeys: []string{identity.ProviderKey(identity.SchemeISBN, "9780000000222")}, Title: "The Little Prince"},
	}
	insts := []codexbf.Instance{
		{Identifiers: []codexbf.Identifier{{Class: "Isbn", Value: "9780000000111"}}},
		{Identifiers: []codexbf.Identifier{{Class: "Isbn", Value: "9780000000222"}}},
	}
	group := WorkGroup{Work: codexbf.Work{Class: "Text", Titles: []codexbf.Title{{MainTitle: "Le Petit Prince"}}}}
	for i, rec := range recs {
		a := r.Resolve(rec)
		if a.WorkID != "wsurv" {
			t.Fatalf("record %d resolved to %s, want wsurv (merge not applied)", i, a.WorkID)
		}
		group.WorkID = a.WorkID
		group.Instances = append(group.Instances, GroupInstance{InstanceID: a.InstanceID, Instance: insts[i]})
	}
	group.Editorial = prior.Editorial["wsurv"]
	if _, err := BuildWorks(sink, []WorkGroup{group}, "overdrive"); err != nil {
		t.Fatalf("re-build: %v", err)
	}
	for _, id := range RetiredWorks(prior.Merges) {
		os.Remove(filepath.Join(dir, filepath.FromSlash(GrainPath(id))))
	}
	return filepath.Join(dir, filepath.FromSlash(GrainPath("wsurv")))
}
