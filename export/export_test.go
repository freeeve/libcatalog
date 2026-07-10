package export

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/freeeve/libcat/bibframe"
	"github.com/freeeve/libcat/ingest"
	"github.com/freeeve/libcat/ingest/marc"
	codex "github.com/freeeve/libcodex"
)

// bookRecord builds a minimal monograph MARC record: 001 control number, 020
// ISBN, 100 author, 245 title, plus any extra data fields.
func bookRecord(control, isbn, author, title string, extra ...codex.Field) *codex.Record {
	r := codex.NewRecord()
	r.SetLeader(codex.Leader([]byte("00000nam a2200000 a 4500")))
	r.AddField(codex.NewControlField("001", control))
	r.AddField(codex.NewDataField("020", ' ', ' ', codex.NewSubfield('a', isbn)))
	r.AddField(codex.NewDataField("100", '1', ' ', codex.NewSubfield('a', author)))
	r.AddField(codex.NewDataField("245", '1', '0', codex.NewSubfield('a', title)))
	for _, f := range extra {
		r.AddField(f)
	}
	return r
}

// memProvider yields pre-built records, so a test corpus needs no fixture file
// and can carry records an ISO 2709 file could not round-trip.
type memProvider struct{ recs []ingest.Record }

func (memProvider) Name() string                                       { return "marc" }
func (memProvider) Role() ingest.Role                                  { return ingest.RoleIngest }
func (p memProvider) Records(context.Context) ([]ingest.Record, error) { return p.recs, nil }

// corpus ingests recs into a fresh temp dir and returns its root.
func corpus(t *testing.T, recs ...*codex.Record) string {
	t.Helper()
	dir := t.TempDir()
	if _, err := ingest.Run(memProvider{recs: marc.FromCodexRecords(recs)}, dir); err != nil {
		t.Fatal(err)
	}
	return dir
}

// plantSourcesQuad appends a provenance attribution to some Work's grain, the
// way an enrichment pass leaves one. It goes in a grain, not in catalog.nq:
// since tasks/298 catalog.nq is the merge of the grains, and since tasks/304 the
// export derives the nq download from them rather than copying that file, so a
// quad that exists only in catalog.nq exists nowhere the exporter looks.
func plantSourcesQuad(t *testing.T, root, sources string) {
	t.Helper()
	paths, err := grainPaths(root)
	if err != nil || len(paths) == 0 {
		t.Fatalf("no grains under %s: %v", root, err)
	}
	id := strings.TrimSuffix(filepath.Base(paths[0]), ".nq")
	quad := "<" + bibframe.WorkIRI(id) + "> <" + bibframe.ExtraPred + "sources> \"" + sources + "\" <editorial:> .\n"
	f, err := os.OpenFile(paths[0], os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(quad); err != nil {
		t.Fatal(err)
	}
	f.Close()
}

// gunzip reads a gzipped artifact fully.
func gunzip(t *testing.T, path string) string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	b, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestRunExportsArtifacts covers the happy path: three artifacts with correct
// digests and counts, and the public-sources allowlist applied to the nq
// download only when set.
func TestRunExportsArtifacts(t *testing.T) {
	in := corpus(t,
		bookRecord("c1", "9780000000011", "Author, A.", "First Book"),
		bookRecord("c2", "9780000000028", "Author, B.", "Second Book"),
	)
	// A provenance extra like an enrichment pass would leave: one public
	// source, one community source that must not leak into the download.
	plantSourcesQuad(t, in, "loc, mombian")

	out := t.TempDir()
	var log bytes.Buffer
	m, err := Run(Options{In: in, Out: out, PublicSources: map[string]bool{"loc": true}, Log: &log})
	if err != nil {
		t.Fatal(err)
	}
	if m.Works != 2 || len(m.Files) != 3 {
		t.Fatalf("manifest works=%d files=%d", m.Works, len(m.Files))
	}
	for _, mf := range m.Files {
		b, err := os.ReadFile(filepath.Join(out, mf.Name))
		if err != nil {
			t.Fatal(err)
		}
		if int64(len(b)) != mf.Bytes {
			t.Fatalf("%s: bytes=%d manifest=%d", mf.Name, len(b), mf.Bytes)
		}
		sum := sha256.Sum256(b)
		if hex.EncodeToString(sum[:]) != mf.SHA256 {
			t.Fatalf("%s: sha256 mismatch", mf.Name)
		}
	}
	if m.Files[1].Records != 2 || m.Files[2].Records != 2 {
		t.Fatalf("MARC record counts = %d/%d, want 2/2", m.Files[1].Records, m.Files[2].Records)
	}
	nq := gunzip(t, filepath.Join(out, "catalog.nq.gz"))
	if strings.Contains(nq, "mombian") {
		t.Fatalf("community source leaked into nq download")
	}
	if !strings.Contains(nq, `"loc"`) {
		t.Fatalf("public source stripped from nq download:\n%s", nq)
	}
	if !strings.Contains(gunzip(t, filepath.Join(out, "catalog.xml.gz")), "First Book") {
		t.Fatalf("marcxml artifact missing record data")
	}
}

// TestRunKeepsSourcesWithoutAllowlist checks nil PublicSources passes the
// corpus through untouched.
func TestRunKeepsSourcesWithoutAllowlist(t *testing.T) {
	in := corpus(t, bookRecord("c1", "9780000000011", "Author, A.", "First Book"))
	plantSourcesQuad(t, in, "loc, mombian")

	out := t.TempDir()
	if _, err := Run(Options{In: in, Out: out, Log: io.Discard}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(gunzip(t, filepath.Join(out, "catalog.nq.gz")), "loc, mombian") {
		t.Fatalf("nq download altered without an allowlist")
	}
}

// TestRunSkipsUnencodableRecords checks the ISO 2709 lockstep skip: a record
// with a field over the 9,999-byte cap drops from both MARC artifacts, the
// export finishes, and the manifest counts only encodable records.
func TestRunSkipsUnencodableRecords(t *testing.T) {
	big := codex.NewDataField("520", ' ', ' ', codex.NewSubfield('a', strings.Repeat("x", 12000)))
	in := corpus(t,
		bookRecord("c1", "9780000000011", "Author, A.", "Keeper"),
		bookRecord("c2", "9780000000028", "Author, B.", "Oversized", big),
	)
	out := t.TempDir()
	var log bytes.Buffer
	m, err := Run(Options{In: in, Out: out, Log: &log})
	if err != nil {
		t.Fatal(err)
	}
	if m.Files[1].Records != 1 || m.Files[2].Records != 1 {
		t.Fatalf("record counts = %d/%d, want 1/1", m.Files[1].Records, m.Files[2].Records)
	}
	if !strings.Contains(log.String(), "skipping") {
		t.Fatalf("no skip warning logged: %q", log.String())
	}
	xml := gunzip(t, filepath.Join(out, "catalog.xml.gz"))
	if !strings.Contains(xml, "Keeper") || strings.Contains(xml, "Oversized") {
		t.Fatalf("artifacts not in lockstep with the skip")
	}
}

// TestRunRefusesEmptyCorpus checks the no-grains guard.
func TestRunRefusesEmptyCorpus(t *testing.T) {
	if _, err := Run(Options{In: t.TempDir(), Out: t.TempDir(), Log: io.Discard}); err == nil {
		t.Fatal("want error for a corpus with no grains")
	}
}

// FuzzFilterSourcesQuad checks the line rewriter never panics and passes
// through lines without a quoted literal unchanged.
func FuzzFilterSourcesQuad(f *testing.F) {
	f.Add(`<urn:w> <p> "loc, mombian" <g> .`)
	f.Add(`<urn:w> <p> "loc"`)
	f.Add(`no quotes here`)
	f.Add(`"`)
	f.Add(`","`)
	f.Fuzz(func(t *testing.T, line string) {
		public := map[string]bool{"loc": true}
		got := filterSourcesQuad(line, public)
		if !strings.Contains(line, `"`) && got != line {
			t.Fatalf("line without literal changed: %q -> %q", line, got)
		}
		if got != "" && got != line && !strings.Contains(got, "loc") {
			t.Fatalf("rewritten line kept nothing allowlisted: %q -> %q", line, got)
		}
	})
}

// tasks/298 warned when export gzipped a catalog.nq written by a pre-v0.120 lcat,
// because the download inherited that file's churning sha256. tasks/304 retired
// the warning by retiring its cause: the nq download is now built from the grains,
// so a stale -- or absent, or hand-corrupted -- catalog.nq cannot reach a reader.
//
// This replaces both warning tests. It asserts the property they were guarding,
// rather than the diagnostic they emitted.
func TestNQDownloadIgnoresTheCatalogNQOnDisk(t *testing.T) {
	in := corpus(t,
		bookRecord("c1", "9780000000011", "Author, A.", "First Book"),
		bookRecord("c2", "9780000000028", "Author, B.", "Second Book"),
	)
	good := t.TempDir()
	want, err := Run(Options{In: in, Out: good, Log: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	wantNQ := gunzip(t, filepath.Join(good, "catalog.nq.gz"))

	// A pre-v0.120 dump: traversal labels, a subject no grain holds, and nothing
	// of the real corpus in it.
	stale := "<urn:test:ghost> <http://ex.org/p> _:b1 <feed:marc> .\n_:b1 <http://ex.org/q> \"leak\" <feed:marc> .\n"
	if err := os.WriteFile(filepath.Join(in, "catalog.nq"), []byte(stale), 0o644); err != nil {
		t.Fatal(err)
	}
	out := t.TempDir()
	got, err := Run(Options{In: in, Out: out, Log: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	gotNQ := gunzip(t, filepath.Join(out, "catalog.nq.gz"))

	if gotNQ != wantNQ {
		t.Errorf("the download changed when catalog.nq did; it is still reading that file")
	}
	if strings.Contains(gotNQ, "urn:test:ghost") || strings.Contains(gotNQ, "leak") {
		t.Error("the stale catalog.nq's own quads reached the download")
	}
	if strings.Contains(gotNQ, "_:b1") {
		t.Error("traversal-order blank labels reached the download")
	}
	if got.Works != want.Works || got.Files[0].SHA256 != want.Files[0].SHA256 {
		t.Errorf("manifest moved: works %d->%d, sha %s -> %s",
			want.Works, got.Works, want.Files[0].SHA256, got.Files[0].SHA256)
	}

	// And with catalog.nq gone entirely.
	if err := os.Remove(filepath.Join(in, "catalog.nq")); err != nil {
		t.Fatal(err)
	}
	none := t.TempDir()
	if _, err := Run(Options{In: in, Out: none, Log: io.Discard}); err != nil {
		t.Fatalf("export needs a catalog.nq it no longer reads: %v", err)
	}
	if gunzip(t, filepath.Join(none, "catalog.nq.gz")) != wantNQ {
		t.Error("the download differs when catalog.nq is absent")
	}
}
