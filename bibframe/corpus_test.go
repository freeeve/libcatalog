package bibframe

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/freeeve/libcat/storage"
	codex "github.com/freeeve/libcodex"
	"github.com/freeeve/libcodex/iso2709"
)

// makeRecord builds a minimal title record with the given control number.
func makeRecord(id, title, author string) *codex.Record {
	r := codex.NewRecord()
	r.SetLeader("00000nam a2200000 a 4500")
	r.AddField(codex.NewControlField("001", id))
	r.AddField(codex.NewDataField("100", '1', ' ', codex.NewSubfield('a', author)))
	r.AddField(codex.NewDataField("245", '1', '0', codex.NewSubfield('a', title)))
	return r
}

// marcBytes encodes records as an ISO 2709 stream.
func marcBytes(t *testing.T, recs []*codex.Record) []byte {
	t.Helper()
	var b bytes.Buffer
	w := iso2709.NewWriter(&b)
	for _, r := range recs {
		if err := w.Write(r); err != nil {
			t.Fatalf("encode marc: %v", err)
		}
	}
	return b.Bytes()
}

// grainOnDisk resolves a work id to its concrete path under a local Dir sink.
func grainOnDisk(root, id string) string {
	return filepath.Join(root, filepath.FromSlash(GrainPath(id)))
}

func TestBuildMARC(t *testing.T) {
	recs := []*codex.Record{
		makeRecord("od-001", "The Argonauts", "Nelson, Maggie"),
		makeRecord("od-002", "Stone Butch Blues", "Feinberg, Leslie"),
	}
	marc := marcBytes(t, recs)

	out := t.TempDir()
	stats, err := BuildMARC(storage.Dir(out), bytes.NewReader(marc), "overdrive")
	if err != nil {
		t.Fatalf("BuildMARC: %v", err)
	}
	if stats.Records != 2 || stats.Grains != 2 {
		t.Fatalf("stats = %+v, want 2 records / 2 grains", stats)
	}

	// Each grain lands at its sharded path and is a canonical feed grain.
	g1, err := os.ReadFile(grainOnDisk(out, "od-001"))
	if err != nil {
		t.Fatalf("read grain od-001: %v", err)
	}
	for _, want := range []string{"The Argonauts", "feed:overdrive", "_:c14n"} {
		if !bytes.Contains(g1, []byte(want)) {
			t.Errorf("grain od-001 missing %q:\n%s", want, g1)
		}
	}

	// catalog.nq covers the whole corpus.
	cat, err := os.ReadFile(filepath.Join(out, "catalog.nq"))
	if err != nil {
		t.Fatalf("read catalog.nq: %v", err)
	}
	for _, want := range []string{"The Argonauts", "Stone Butch Blues", "feed:overdrive"} {
		if !bytes.Contains(cat, []byte(want)) {
			t.Errorf("catalog.nq missing %q", want)
		}
	}

	// The build is deterministic: a second run from the same MARC is byte-identical.
	out2 := t.TempDir()
	if _, err := BuildMARC(storage.Dir(out2), bytes.NewReader(marc), "overdrive"); err != nil {
		t.Fatalf("BuildMARC (2nd): %v", err)
	}
	assertSameFile(t, grainOnDisk(out, "od-001"), grainOnDisk(out2, "od-001"))
	assertSameFile(t, filepath.Join(out, "catalog.nq"), filepath.Join(out2, "catalog.nq"))
}

func assertSameFile(t *testing.T, a, b string) {
	t.Helper()
	ba, err := os.ReadFile(a)
	if err != nil {
		t.Fatalf("read %s: %v", a, err)
	}
	bb, err := os.ReadFile(b)
	if err != nil {
		t.Fatalf("read %s: %v", b, err)
	}
	if !bytes.Equal(ba, bb) {
		t.Errorf("files differ across builds (non-deterministic):\n%s\n---\n%s", ba, bb)
	}
}
