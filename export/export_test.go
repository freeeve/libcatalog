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
	quad := "<urn:test:w1> <" + bibframe.ExtraPred + "sources> \"loc, mombian\" <feed:marc> .\n"
	f, err := os.OpenFile(filepath.Join(in, "catalog.nq"), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(quad); err != nil {
		t.Fatal(err)
	}
	f.Close()

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
	quad := "<urn:test:w1> <" + bibframe.ExtraPred + "sources> \"loc, mombian\" <feed:marc> .\n"
	f, err := os.OpenFile(filepath.Join(in, "catalog.nq"), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(quad); err != nil {
		t.Fatal(err)
	}
	f.Close()

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

// tasks/298: export gzips a catalog.nq it did not write. Every writer now emits
// grain-derived labels, but a tree left by an older lcat still holds the churning
// dump, and exporting it silently republishes a moving sha256. Say so once.
func TestExportWarnsAboutAStaleCatalogNQ(t *testing.T) {
	in := corpus(t, bookRecord("c1", "9780000000011", "Author, A.", "First Book"))
	// Overwrite catalog.nq the way a pre-v0.120 lcat left it: traversal labels.
	stale := "<urn:test:w1> <http://ex.org/p> _:b1 <feed:marc> .\n_:b1 <http://ex.org/q> \"x\" <feed:marc> .\n"
	if err := os.WriteFile(filepath.Join(in, "catalog.nq"), []byte(stale), 0o644); err != nil {
		t.Fatal(err)
	}
	var log bytes.Buffer
	if _, err := Run(Options{In: in, Out: t.TempDir(), Log: &log}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(log.String(), "traversal-order blank-node labels") {
		t.Errorf("no warning for a stale catalog.nq; log was %q", log.String())
	}
	if !strings.Contains(log.String(), "lcat serialize") {
		t.Errorf("the warning does not say how to fix it: %q", log.String())
	}
}

// The warning must not fire on what ingest writes today, or it is noise that
// teaches operators to ignore it.
func TestExportDoesNotWarnAboutAFreshCatalogNQ(t *testing.T) {
	in := corpus(t,
		bookRecord("c1", "9780000000011", "Author, A.", "First Book"),
		bookRecord("c2", "9780000000028", "Author, B.", "Second Book"),
	)
	nq, err := os.ReadFile(filepath.Join(in, "catalog.nq"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(nq, []byte("_:")) {
		t.Skip("fixture corpus has no blank nodes, so this proves nothing")
	}
	var log bytes.Buffer
	if _, err := Run(Options{In: in, Out: t.TempDir(), Log: &log}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(log.String(), "traversal-order") {
		t.Errorf("warned about a catalog.nq ingest just wrote: %q", log.String())
	}
}
