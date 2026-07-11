package bibframe

import (
	"os"
	"strings"
	"testing"

	codex "github.com/freeeve/libcodex"
	codexbf "github.com/freeeve/libcodex/bibframe"
	"github.com/freeeve/libcodex/rdf"
)

func TestVerbatimFieldRoundTrip(t *testing.T) {
	fields := []codex.Field{
		codex.NewControlField("001", "ODN0000123"),
		codex.NewDataField("037", 'a', ' ',
			codex.NewSubfield('a', "12345-67"),
			codex.NewSubfield('b', "OverDrive, Inc."),
			codex.NewSubfield('n', "http://www.overdrive.com"),
		),
		codex.NewDataField("040", ' ', ' ', codex.NewSubfield('a', "TEFOD")),
	}
	for _, f := range fields {
		enc := EncodeVerbatimField(f)
		back, err := DecodeVerbatimField(enc)
		if err != nil {
			t.Fatalf("decode %q: %v", enc, err)
		}
		if back.Tag != f.Tag || back.Value != f.Value || len(back.Subfields) != len(f.Subfields) {
			t.Fatalf("round trip %q:\n got %+v\nwant %+v", enc, back, f)
		}
		for i, sf := range f.Subfields {
			if back.Subfields[i] != sf {
				t.Fatalf("subfield %d: got %+v want %+v", i, back.Subfields[i], sf)
			}
		}
		if !f.IsControl() {
			i1, i2 := f.Indicators()
			b1, b2 := back.Indicators()
			if b1 != i1 || b2 != i2 {
				t.Fatalf("indicators: got %c%c want %c%c", b1, b2, i1, i2)
			}
		}
	}
	if _, err := DecodeVerbatimField("04"); err == nil {
		t.Fatal("short verbatim accepted")
	}
}

// marcGrainFixture builds a grain the way MARC ingest does: the vendored
// MARC Express sample crosswalked via FromRecord, plus its known-loss
// fields as the verbatim sidecar.
func marcGrainFixture(t *testing.T) ([]byte, *codex.Record) {
	t.Helper()
	f, err := os.Open("../ingest/overdrive/testdata/marc-express/od-sample-ebook.mrc")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	recs, err := ReadMARC(f)
	if err != nil || len(recs) == 0 {
		t.Fatalf("read sample: %v (%d records)", err, len(recs))
	}
	rec := recs[0]
	bib := codexbf.FromRecord(rec)
	wi := codexbf.WorkInstances{Work: bib.Work, Instances: []codexbf.Instance{bib.Instance}}
	g := wi.Graph("wmarc00000001", []string{"imarc00000001"})
	addInstanceVerbatim(g, "imarc00000001", VerbatimFields(rec))
	grain, err := GrainFromGraph(g, FeedGraph("marc"))
	if err != nil {
		t.Fatal(err)
	}
	return grain, rec
}

// TestDecodeGrainMARCVerbatim proves the sidecar round-trip: the known-loss
// tags dropped by the crosswalk come back field-exact on export.
func TestDecodeGrainMARCVerbatim(t *testing.T) {
	grain, orig := marcGrainFixture(t)
	if !strings.Contains(string(grain), PredMARCVerbatim) {
		t.Fatalf("sidecar missing from grain:\n%.500s", grain)
	}
	recs, err := DecodeGrainMARC(grain)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 {
		t.Fatalf("decoded %d records", len(recs))
	}
	for tag := range KnownLoss {
		want := orig.DataFields(tag)
		got := recs[0].DataFields(tag)
		if len(want) != len(got) {
			t.Errorf("tag %s: %d in original, %d after sidecar round-trip", tag, len(want), len(got))
			continue
		}
		for i := range want {
			if EncodeVerbatimField(want[i]) != EncodeVerbatimField(got[i]) {
				t.Errorf("tag %s[%d]: got %+v want %+v", tag, i, got[i], want[i])
			}
		}
	}
	// A plain libcodex decode of the same grain drops them (the sidecar is
	// the difference).
	if _, lossy := LossyTag("037"); lossy && len(orig.DataFields("037")) > 0 {
		plain, err := codexbf.Decode(grain)
		if err != nil {
			t.Fatal(err)
		}
		if len(plain[0].DataFields("037")) != 0 {
			t.Fatal("037 survives a plain decode; the loss table is stale")
		}
	}
}

// TestDecodeGrainMARCSameAs758 proves the external-identity link now reaches MARC:
// libcodex v0.29.0's crosswalk (requested via libcodex 121) turns a
// Work's owl:sameAs -- the hub URI the identity enrichment writes -- into a MARC 758
// Resource Identifier with the URI in $1. Closes the KnownLoss 758 gap.
func TestDecodeGrainMARCSameAs758(t *testing.T) {
	grain, _ := marcGrainFixture(t)
	const olURI = "https://openlibrary.org/works/OL45804W"
	work := WorkIRI("wmarc00000001")
	line := "<" + work + "> <http://www.w3.org/2002/07/owl#sameAs> <" + olURI + "> <enrichment:openlibrary> .\n"
	recs, err := DecodeGrainMARC(append(append([]byte{}, grain...), line...))
	if err != nil {
		t.Fatal(err)
	}
	got := recs[0].SubfieldValues("758", '1')
	if len(got) != 1 || got[0] != olURI {
		t.Fatalf("758 $1 = %v, want [%s] -- owl:sameAs must decode to MARC 758 (libcodex v0.29.0)", got, olURI)
	}
}

// TestDecodeGrainMARCShadow proves lcat:overrides changes what decodes: an
// editorial claim on a predicate hides the feed value and shows the
// editorial replacement.
func TestDecodeGrainMARCShadow(t *testing.T) {
	grain, _ := marcGrainFixture(t)
	recs, err := DecodeGrainMARC(grain)
	if err != nil {
		t.Fatal(err)
	}
	const summaryPred = "http://id.loc.gov/ontologies/bibframe/summary"
	if recs[0].SubfieldValue("520", 'a') == "" {
		t.Skip("sample carries no 520; shadow test needs one")
	}
	work := WorkIRI("wmarc00000001")
	// Claim bf:summary on the Work (where the crosswalk puts 520) and
	// re-assert editorially.
	patch := OverridePatch(work, summaryPred)
	patch.Add = append(patch.Add, rdf.Quad{
		S: rdf.NewIRI(work), P: rdf.NewIRI(summaryPred), O: rdf.NewLiteral("An edited summary.", "", ""),
	})
	updated, err := ApplyEditorialPatch(grain, patch)
	if err != nil {
		t.Fatal(err)
	}
	recs, err = DecodeGrainMARC(updated)
	if err != nil {
		t.Fatal(err)
	}
	vals := recs[0].SubfieldValues("520", 'a')
	if len(vals) != 1 || vals[0] != "An edited summary." {
		t.Fatalf("shadowed 520 = %v", vals)
	}
}
