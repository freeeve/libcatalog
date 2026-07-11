package bibframe

import (
	"os"
	"testing"

	codex "github.com/freeeve/libcodex"
	codexbf "github.com/freeeve/libcodex/bibframe"
)

// TestDecodeGrainMARCSource covers the derivation: an unedited
// arrival keeps its 040 untouched, an edited grain appends the deployment
// as one $d, a born-digital grain (no 040) synthesizes $a/$c, and an empty
// org code changes nothing.
func TestDecodeGrainMARCSource(t *testing.T) {
	grain, orig := marcGrainFixture(t)
	want040 := orig.DataFields("040")
	if len(want040) != 1 {
		t.Fatalf("sample carries %d 040s", len(want040))
	}

	// Unedited arrival: field-exact, no $d appended.
	recs, err := DecodeGrainMARCSource(grain, "QLL")
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := recs[0].DataField("040"); len(got.SubfieldValues('d')) != len(want040[0].SubfieldValues('d')) {
		t.Fatalf("unedited grain grew a $d: %+v", got)
	}

	// Edited grain: the deployment joins the modifying-agency chain once.
	edited := append(append([]byte(nil), grain...),
		[]byte("<#wmarc00000001Work> <http://purl.org/dc/terms/title> \"edited\" <editorial:> .\n")...)
	recs, err = DecodeGrainMARCSource(edited, "QLL")
	if err != nil {
		t.Fatal(err)
	}
	got, _ := recs[0].DataField("040")
	ds := got.SubfieldValues('d')
	if len(ds) == 0 || ds[len(ds)-1] != "QLL" {
		t.Fatalf("edited grain 040 missing trailing $d: %+v", got)
	}
	// $d stays ahead of any $e conventions.
	seenD := false
	for _, sf := range got.Subfields {
		if sf.Code == 'd' {
			seenD = true
		}
		if sf.Code == 'e' && !seenD {
			t.Fatalf("$d landed after $e: %+v", got.Subfields)
		}
	}
	// Idempotent when the deployment already trails the chain.
	applyCatalogingSource(recs[0], "QLL", true)
	if again, _ := recs[0].DataField("040"); len(again.SubfieldValues('d')) != len(ds) {
		t.Fatalf("repeated apply grew the chain: %+v", again)
	}

	// Empty org code: derivation disabled.
	recs, err = DecodeGrainMARCSource(edited, "")
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := recs[0].DataField("040"); len(got.SubfieldValues('d')) != len(want040[0].SubfieldValues('d')) {
		t.Fatalf("empty org still derived: %+v", got)
	}

	// Born-digital (no 040 anywhere): synthesize $a/$c.
	f, err := os040FreeGrain(t)
	if err != nil {
		t.Fatal(err)
	}
	recs, err = DecodeGrainMARCSource(f, "QLL")
	if err != nil {
		t.Fatal(err)
	}
	got, ok := recs[0].DataField("040")
	if !ok || got.SubfieldValue('a') != "QLL" || got.SubfieldValue('c') != "QLL" {
		t.Fatalf("born-digital 040 = %+v (ok=%v)", got, ok)
	}
}

// os040FreeGrain builds a grain with no cataloging source, the born-digital
// provider shape: the sample's crosswalk output minus AdminMetadata and
// minus the verbatim sidecar.
func os040FreeGrain(t *testing.T) ([]byte, error) {
	t.Helper()
	f, err := os.Open("../ingest/overdrive/testdata/marc-express/od-sample-ebook.mrc")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	recs, err := ReadMARC(f)
	if err != nil || len(recs) == 0 {
		t.Fatalf("read sample: %v", err)
	}
	bib := codexbf.FromRecord(recs[0])
	bib.Instance.Admin = nil
	wi := codexbf.WorkInstances{Work: bib.Work, Instances: []codexbf.Instance{bib.Instance}}
	g := wi.Graph("wborn00000001", []string{"iborn00000001"})
	return GrainFromGraph(g, FeedGraph("overdrive"))
}

var _ = codex.NewSubfield
