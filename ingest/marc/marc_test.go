package marc

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/freeeve/libcatalog/ingest"
	codex "github.com/freeeve/libcodex"
	codexbf "github.com/freeeve/libcodex/bibframe"
	"github.com/freeeve/libcodex/iso2709"
)

// bookRecord builds a minimal monograph MARC record: 001 control number, 020 ISBN,
// 100 author, 245 title.
func bookRecord(control, isbn, author, title string) *codex.Record {
	r := codex.NewRecord()
	r.SetLeader(codex.Leader([]byte("00000nam a2200000 a 4500")))
	r.AddField(codex.NewControlField("001", control))
	if isbn != "" {
		r.AddField(codex.NewDataField("020", ' ', ' ', codex.NewSubfield('a', isbn)))
	}
	r.AddField(codex.NewDataField("100", '1', ' ', codex.NewSubfield('a', author)))
	r.AddField(codex.NewDataField("245", '1', '0', codex.NewSubfield('a', title)))
	return r
}

// writeMARC encodes records to a temp .mrc file and returns its path.
func writeMARC(t *testing.T, recs ...*codex.Record) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "in.mrc")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	w := iso2709.NewWriter(f)
	for _, r := range recs {
		if err := w.Write(r); err != nil {
			t.Fatal(err)
		}
	}
	f.Close()
	return path
}

func readNQuads(t *testing.T, dir string) string {
	t.Helper()
	var b strings.Builder
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".nq") || d.Name() == "catalog.nq" {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		b.Write(data)
		return nil
	})
	if err != nil {
		t.Fatalf("read grains: %v", err)
	}
	return b.String()
}

// TestCleanFreeText pins the free-text normalization: HTML character
// references decode and markup strips from 520/505/5xx prose, while text
// without markup (including "<" as prose) passes through byte-identical.
func TestCleanFreeText(t *testing.T) {
	rec := bookRecord("c1", "9780000000001", "Shakespeare, William", "Hamlet &#8212; Prince of Denmark")
	rec.AddField(codex.NewDataField("520", ' ', ' ',
		codex.NewSubfield('a', "at the hand of his uncle Claudius &#8212; but <b>Hamlet&#39;s</b> spiral<br/> into grief")))
	rec.AddField(codex.NewDataField("505", '0', ' ',
		codex.NewSubfield('a', "Act I &amp; II -- Act III")))
	rec.AddField(codex.NewDataField("500", ' ', ' ',
		codex.NewSubfield('a', "Includes &quot;annotations&quot;")))
	out := FromCodexRecords([]*codex.Record{rec})
	if len(out) != 1 {
		t.Fatalf("records = %d", len(out))
	}
	work, inst := out[0].Work(), out[0].Instance()
	// Transcribed titles decode entities and strip markup (tasks/081).
	if got, want := work.Titles[0].MainTitle, "Hamlet — Prince of Denmark"; got != want {
		t.Errorf("work title = %q, want %q", got, want)
	}
	if got, want := inst.Titles[0].MainTitle, "Hamlet — Prince of Denmark"; got != want {
		t.Errorf("instance title = %q, want %q", got, want)
	}
	if got, want := work.Summary[0], "at the hand of his uncle Claudius — but Hamlet's spiral into grief"; got != want {
		t.Errorf("summary = %q, want %q", got, want)
	}
	if got, want := work.TableOfContents[0], "Act I & II -- Act III"; got != want {
		t.Errorf("toc = %q, want %q", got, want)
	}
	if got, want := inst.Notes[0].Label, `Includes "annotations"`; got != want {
		t.Errorf("note = %q, want %q", got, want)
	}
	if got, want := ingest.CleanText("plain prose, 2 < 3, no markup"), "plain prose, 2 < 3, no markup"; got != want {
		t.Errorf("prose = %q, want %q", got, want)
	}
	// Double-escaped vendor text decodes to a fixpoint; registered-trademark ref decodes.
	if got, want := ingest.CleanText("friendship&amp;#8212;a Newbery Honor Book!"), "friendship—a Newbery Honor Book!"; got != want {
		t.Errorf("double-escaped = %q, want %q", got, want)
	}
	if got, want := ingest.CleanText("LEGO&#174; Creations"), "LEGO® Creations"; got != want {
		t.Errorf("trademark = %q, want %q", got, want)
	}
}

// TestMARCProviderClustersAndRoutes runs a MARC file through the shared pipeline:
// two records sharing an author+title cluster into one Work (distinct ISBNs, so
// only the cluster key merges them), a third stays separate, and every grain lands
// in feed:marc. This is what the legacy per-record BuildMARC path could not do.
func TestMARCProviderClustersAndRoutes(t *testing.T) {
	path := writeMARC(t,
		bookRecord("m1", "9780000000001", "Doe, Jane", "Shared Title"),
		bookRecord("m2", "9780000000002", "Doe, Jane", "Shared Title"),
		bookRecord("m3", "9780000000003", "Roe, Rick", "Other Title"),
	)
	prov, err := New(ingest.Config{Source: path})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if prov.Name() != "marc" || prov.Role() != ingest.RoleIngest {
		t.Fatalf("name/role = %q/%v", prov.Name(), prov.Role())
	}

	out := t.TempDir()
	res, err := ingest.Run(prov, out)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	// 3 records -> 3 instances clustered into 2 works.
	if res.Stats.Grains != 2 || res.Stats.Records != 3 {
		t.Errorf("works/instances = %d/%d, want 2/3", res.Stats.Grains, res.Stats.Records)
	}
	if res.MintedWorks != 2 || res.MintedInstances != 3 {
		t.Errorf("minted works/instances = %d/%d, want 2/3", res.MintedWorks, res.MintedInstances)
	}
	nq := readNQuads(t, out)
	if !strings.Contains(nq, "<feed:marc>") {
		t.Errorf("grains missing feed:marc graph:\n%s", nq)
	}
	if !strings.Contains(nq, "Shared Title") || !strings.Contains(nq, "Other Title") {
		t.Error("grains missing expected titles")
	}
}

// TestMARCProviderReingestStable proves re-ingesting the same MARC file mints
// nothing and rewrites byte-identical grains (the tasks/002 no-churn gate, now via
// the MARC provider).
func TestMARCProviderReingestStable(t *testing.T) {
	path := writeMARC(t,
		bookRecord("m1", "9780000000001", "Doe, Jane", "A Title"),
		bookRecord("m2", "9780000000002", "Roe, Rick", "B Title"),
	)
	prov, _ := New(ingest.Config{Source: path})
	out := t.TempDir()

	if _, err := ingest.Run(prov, out); err != nil {
		t.Fatalf("first Run: %v", err)
	}
	before := readNQuads(t, out)

	second, err := ingest.Run(prov, out)
	if err != nil {
		t.Fatalf("second Run: %v", err)
	}
	if second.MintedWorks != 0 || second.MintedInstances != 0 {
		t.Errorf("re-ingest minted %d/%d, want 0/0", second.MintedWorks, second.MintedInstances)
	}
	if readNQuads(t, out) != before {
		t.Error("re-ingest changed grains")
	}
}

// TestFeedOverride confirms Config.Feed overrides the provenance graph (an ILS names
// its own feed, e.g. feed:sierra).
func TestFeedOverride(t *testing.T) {
	path := writeMARC(t, bookRecord("m1", "9780000000001", "Doe, Jane", "T"))
	prov, _ := New(ingest.Config{Source: path, Feed: "sierra"})
	out := t.TempDir()
	if _, err := ingest.Run(prov, out); err != nil {
		t.Fatal(err)
	}
	nq := readNQuads(t, out)
	if !strings.Contains(nq, "<feed:sierra>") || strings.Contains(nq, "<feed:marc>") {
		t.Errorf("feed override not applied:\n%s", nq)
	}
}

// TestNewRequiresSource guards the factory contract.
func TestNewRequiresSource(t *testing.T) {
	if _, err := New(ingest.Config{}); err == nil {
		t.Error("New without a source should error")
	}
}

// TestRecordIdentity checks key derivation: the control number resolves first, ISBN
// is the cross-provider merge key, and the cluster fields come off the BIBFRAME.
func TestRecordIdentity(t *testing.T) {
	bib := codexbf.FromRecord(bookRecord("m9", "9781234567897", "Le Guin, Ursula K.", "A Wizard of Earthsea"))
	id := recordIdentity(bib, "m9")
	if id.Title != "A Wizard of Earthsea" {
		t.Errorf("title = %q", id.Title)
	}
	if id.Author != "Le Guin, Ursula K." {
		t.Errorf("author = %q", id.Author)
	}
	if len(id.ProviderKeys) < 2 || id.ProviderKeys[0] != "id:m9" {
		t.Errorf("keys = %v, want control number first", id.ProviderKeys)
	}
	var hasISBN bool
	for _, k := range id.ProviderKeys {
		if k == "isbn:9781234567897" {
			hasISBN = true
		}
	}
	if !hasISBN {
		t.Errorf("keys = %v, missing isbn merge key", id.ProviderKeys)
	}
}
