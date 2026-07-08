package bibframe

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/freeeve/libcat/storage"
	codexbf "github.com/freeeve/libcodex/bibframe"
	"github.com/freeeve/libcodex/rdf"
)

// TestSerializeGrains checks that regenerating catalog.nq from committed grains
// merges every Work, stays valid N-Quads with collision-free blank labels, and is
// byte-stable across runs (a clean re-serialize diff).
func TestSerializeGrains(t *testing.T) {
	dir := t.TempDir()
	sink := storage.Dir(dir)

	works := []WorkGroup{
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
	if _, err := BuildWorks(sink, works, "overdrive"); err != nil {
		t.Fatalf("build: %v", err)
	}

	// Regenerate catalog.nq purely from the grains (no re-ingest).
	n, err := SerializeGrains(dir, sink)
	if err != nil {
		t.Fatalf("SerializeGrains: %v", err)
	}
	if n != 2 {
		t.Fatalf("serialized %d grains, want 2", n)
	}
	catalogPath := filepath.Join(dir, "catalog.nq")
	got := readFile(t, catalogPath)

	// Valid N-Quads carrying both Works and both ISBNs.
	ds, err := rdf.ParseNQuads(got)
	if err != nil {
		t.Fatalf("serialized catalog.nq is not valid N-Quads: %v", err)
	}
	if works := ds.Graph(FeedGraph("overdrive")).SubjectsOfType("http://id.loc.gov/ontologies/bibframe/Work"); len(works) != 2 {
		t.Errorf("feed graph has %d Works, want 2", len(works))
	}
	for _, want := range []string{"9780000000001", "9780000000002", "#woneWork", "#wtwoWork"} {
		if !bytes.Contains(got, []byte(want)) {
			t.Errorf("catalog.nq missing %q", want)
		}
	}

	// Re-serialize is byte-identical (clean diff).
	if _, err := SerializeGrains(dir, sink); err != nil {
		t.Fatalf("SerializeGrains (again): %v", err)
	}
	if again := readFile(t, catalogPath); !bytes.Equal(got, again) {
		t.Error("SerializeGrains is not byte-stable across runs")
	}

	// catalog.nq is excluded from its own input on the next run (no doubling).
	if _, err := os.Stat(catalogPath); err != nil {
		t.Fatal(err)
	}
	if n2, err := SerializeGrains(dir, sink); err != nil || n2 != 2 {
		t.Errorf("re-serialize grain count = %d (err %v), want 2 (catalog.nq must be skipped)", n2, err)
	}
}
