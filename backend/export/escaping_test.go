package export

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	codex "github.com/freeeve/libcodex"
	"github.com/freeeve/libcodex/iso2709"

	"github.com/freeeve/libcat/ingest"
	"github.com/freeeve/libcat/ingest/marc"
	"github.com/freeeve/libcat/storage/blob"
)

// TestExportMARCEmitsRawFreeText is the regression: ISO 2709 is a
// binary format, not XML, so exported free text must be raw bytes -- never
// XML-escaped. A grain whose prose carries a literal "&" or an em-dash must
// export as those bytes, not "&amp;" or "&#8212;". It also confirms the
// ingest cleaning reached the grain (markup stripped, entity decoded).
func TestExportMARCEmitsRawFreeText(t *testing.T) {
	rec := codex.NewRecord()
	rec.SetLeader(codex.Leader([]byte("00000nam a2200000 a 4500")))
	rec.AddField(codex.NewControlField("001", "raw1"))
	rec.AddField(codex.NewDataField("020", ' ', ' ', codex.NewSubfield('a', "9780000000123")))
	rec.AddField(codex.NewDataField("100", '1', ' ', codex.NewSubfield('a', "Doe, Jane")))
	rec.AddField(codex.NewDataField("245", '1', '0', codex.NewSubfield('a', "Q&A about Salt &amp; Pepper")))
	rec.AddField(codex.NewDataField("520", ' ', ' ',
		codex.NewSubfield('a', "Sci-fi &amp; fantasy&#8212;featuring <b>heroes</b> &amp; villains")))

	dir := t.TempDir()
	mrc := filepath.Join(dir, "in.mrc")
	f, err := os.Create(mrc)
	if err != nil {
		t.Fatal(err)
	}
	if err := iso2709.NewWriter(f).Write(rec); err != nil {
		t.Fatal(err)
	}
	f.Close()

	site := t.TempDir()
	prov, err := marc.New(ingest.Config{Source: mrc})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ingest.Run(prov, site); err != nil {
		t.Fatal(err)
	}

	bs := blob.NewMem()
	var workIDs []string
	err = filepath.WalkDir(site, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".nq") || d.Name() == "catalog.nq" {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(site, path)
		if _, err := bs.Put(t.Context(), filepath.ToSlash(rel), data, blob.PutOptions{}); err != nil {
			return err
		}
		workIDs = append(workIDs, strings.TrimSuffix(d.Name(), ".nq"))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	svc := newService(t, bs)
	job, err := svc.Create(t.Context(), "lib@example.org", FormatMARC, Selection{WorkIDs: workIDs})
	if err != nil {
		t.Fatal(err)
	}
	out, err := svc.Open(t.Context(), job)
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)

	// No XML-escape artifacts anywhere in the binary record.
	for _, bad := range []string{"&amp;", "&lt;", "&gt;", "&#8212;", "<b>", "</b>"} {
		if strings.Contains(s, bad) {
			t.Errorf("exported .mrc contains XML-escape/markup artifact %q", bad)
		}
	}
	// The genuine content survives as raw bytes: literal ampersands and a decoded
	// em-dash (U+2014, UTF-8 e2 80 94).
	for _, want := range []string{"Q&A", "Salt & Pepper", "Sci-fi & fantasy—featuring heroes & villains"} {
		if !strings.Contains(s, want) {
			t.Errorf("exported .mrc missing raw text %q", want)
		}
	}
}
