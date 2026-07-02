package bibframe

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/freeeve/libcatalog/storage"
	codexbf "github.com/freeeve/libcodex/bibframe"
)

func sampleWork() []WorkGroup {
	return []WorkGroup{{
		WorkID: "wtest1",
		Work:   codexbf.Work{Class: "Text", Titles: []codexbf.Title{{MainTitle: "A Title"}}},
		Instances: []GroupInstance{{
			InstanceID: "itest1",
			Instance:   codexbf.Instance{Identifiers: []codexbf.Identifier{{Class: "Isbn", Value: "9781234567890"}}},
		}},
	}}
}

// A curator-authored subject on the Work, in the editorial graph.
const editorialLine = "<#wtest1Work> <http://id.loc.gov/ontologies/bibframe/subject> " +
	"<https://homosaurus.org/v3/homoit0000669> <editorial:> .\n"

// TestClobberSafeReingest checks that an editorial statement authored into a grain
// survives a feed re-ingest (§5) and that the rewritten grain is byte-stable.
func TestClobberSafeReingest(t *testing.T) {
	dir := t.TempDir()
	sink := storage.Dir(dir)

	if _, err := BuildWorks(sink, sampleWork(), "overdrive"); err != nil {
		t.Fatalf("build: %v", err)
	}
	grainPath := filepath.Join(dir, filepath.FromSlash(GrainPath("wtest1")))

	// A curator appends an editorial subject to the committed grain.
	appendLine(t, grainPath, editorialLine)

	// Re-ingest: the feed is regenerated; the editorial must be recovered and kept.
	prior, err := LoadPrior(dir, "overdrive")
	if err != nil {
		t.Fatalf("load prior: %v", err)
	}
	if len(prior.Editorial["wtest1"]) == 0 {
		t.Fatal("editorial not recovered from grain")
	}
	works := sampleWork()
	works[0].Editorial = prior.Editorial["wtest1"]
	if _, err := BuildWorks(sink, works, "overdrive"); err != nil {
		t.Fatalf("re-build: %v", err)
	}

	got := readFile(t, grainPath)
	for _, want := range []string{"homoit0000669", "<editorial:>", "9781234567890"} {
		if !bytes.Contains(got, []byte(want)) {
			t.Errorf("re-ingest lost %q", want)
		}
	}

	// A second re-ingest (editorial recovered again) is byte-identical.
	prior2, err := LoadPrior(dir, "overdrive")
	if err != nil {
		t.Fatalf("load prior 2: %v", err)
	}
	works = sampleWork()
	works[0].Editorial = prior2.Editorial["wtest1"]
	before := readFile(t, grainPath)
	if _, err := BuildWorks(sink, works, "overdrive"); err != nil {
		t.Fatalf("re-build 2: %v", err)
	}
	if after := readFile(t, grainPath); !bytes.Equal(before, after) {
		t.Error("re-ingest with editorial is not byte-stable")
	}
}

func appendLine(t *testing.T, path, line string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(line); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
