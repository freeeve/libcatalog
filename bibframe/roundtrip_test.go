package bibframe

import (
	"os"
	"testing"

	codexbf "github.com/freeeve/libcodex/bibframe"
)

// tasks/003: MARC <-> BIBFRAME round-trip fidelity as a CI gate. MARC -> BIBFRAME is
// lossy (LC's own converters drop data), so the framework measures the loss and
// pins it rather than assuming fidelity. The harness round-trips the vendored
// OverDrive MARC Express samples (MARC -> BIBFRAME via Encode -> MARC via Decode) and
// asserts two contracts, both backed by docs/marc-fidelity.md:
//   1. every core bibliographic field survives (a regression breaks the build), and
//   2. no field is lost that the known-loss table does not already list (a new,
//      undocumented loss breaks the build).

// marcExpressSamples are the real OverDrive MARC Express records vendored for the
// MARC-import ramp (tasks/007).
var marcExpressSamples = []string{
	"../ingest/overdrive/testdata/marc-express/od-sample-ebook.mrc",
	"../ingest/overdrive/testdata/marc-express/od-sample-audiobook.mrc",
}

// coreFields must survive MARC -> BIBFRAME -> MARC on every sample record that
// carries them: the identifiers, primary/added agents, title, publication, extent,
// carrier, summary, subjects, genre, and access link an adopter judges fidelity by.
var coreFields = []string{
	"001", "006", "007", "008", "020", "100", "245", "250", "260", "300",
	"306", "336", "337", "338", "347", "490", "500", "511", "520", "521",
	"533", "538", "650", "655", "700", "776", "856",
}

// knownLostFields are the tags that do NOT survive the round-trip, measured and
// explained in docs/marc-fidelity.md -- now only the vendor-convention fields,
// which decode to their modeled equivalents (037 -> an 024-shaped identifier,
// 040 -> provenance-out-of-band, 084 -> 072) rather than their original tags:
// deliberate non-goals, not losses of data. The reconstruction arc: libcodex
// v0.9.0 moved 008/336/500 to coreFields (tasks/053), v0.11.0 moved
// 306/347/490/511/521/533/538/776 (tasks/055, upstream 081), and v0.12.0
// finished 006/007 (upstream 082). A round-trip that loses anything NOT in
// this set is an unexplained regression -- and a field listed here that
// survives is a stale table (TestMARCRoundTripLossTableCurrent); update the
// doc and this set together when the crosswalk changes.
var knownLostFields = map[string]bool{
	"037": true, "040": true, "084": true,
}

// roundTripTags round-trips every record in a sample and returns how many times each
// field tag appears in the input vs the round-tripped output.
func roundTripTags(t *testing.T, sample string) (in, out map[string]int) {
	t.Helper()
	f, err := os.Open(sample)
	if err != nil {
		t.Fatalf("open %s: %v", sample, err)
	}
	defer f.Close()
	recs, err := ReadMARC(f)
	if err != nil {
		t.Fatalf("read %s: %v", sample, err)
	}
	in, out = map[string]int{}, map[string]int{}
	for _, rec := range recs {
		data, err := codexbf.Encode(rec)
		if err != nil {
			t.Fatalf("encode: %v", err)
		}
		back, err := codexbf.Decode(data)
		if err != nil {
			t.Fatalf("decode: %v", err)
		}
		if len(back) == 0 {
			t.Fatalf("decode of %s produced no record", sample)
		}
		for _, fld := range rec.Fields() {
			in[fld.Tag]++
		}
		for _, fld := range back[0].Fields() {
			out[fld.Tag]++
		}
	}
	return in, out
}

// TestMARCRoundTripCoreFieldsSurvive fails if a core bibliographic field is dropped
// by the round-trip -- the fidelity guarantee adopters rely on.
func TestMARCRoundTripCoreFieldsSurvive(t *testing.T) {
	for _, sample := range marcExpressSamples {
		in, out := roundTripTags(t, sample)
		for _, tag := range coreFields {
			if in[tag] > 0 && out[tag] == 0 {
				t.Errorf("%s: core field %s present in input but dropped by round-trip", sample, tag)
			}
		}
	}
}

// TestMARCRoundTripNoUndocumentedLoss fails if the round-trip drops any field not in
// the published known-loss table -- so a crosswalk change that quietly loses data
// breaks the build until it is measured and documented.
func TestMARCRoundTripNoUndocumentedLoss(t *testing.T) {
	for _, sample := range marcExpressSamples {
		in, out := roundTripTags(t, sample)
		for tag := range in {
			if out[tag] == 0 && !knownLostFields[tag] {
				t.Errorf("%s: field %s lost by round-trip but not in the known-loss table (docs/marc-fidelity.md); measure and document it", sample, tag)
			}
		}
	}
}

// TestMARCRoundTripLossTableCurrent is the reverse gate: a field listed as
// known-lost that actually survives on every sample means the crosswalk
// improved and the table (and docs/marc-fidelity.md) is overdue for a
// re-measure -- the loss contract must stay honest in both directions.
func TestMARCRoundTripLossTableCurrent(t *testing.T) {
	stillLost := map[string]bool{}
	present := map[string]bool{}
	for _, sample := range marcExpressSamples {
		in, out := roundTripTags(t, sample)
		for tag := range in {
			present[tag] = true
			if out[tag] == 0 {
				stillLost[tag] = true
			}
		}
	}
	for tag := range knownLostFields {
		if present[tag] && !stillLost[tag] {
			t.Errorf("field %s is listed as known-lost but now survives the round-trip; move it to coreFields and update docs/marc-fidelity.md", tag)
		}
	}
}
