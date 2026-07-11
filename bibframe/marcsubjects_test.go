package bibframe

import (
	"strings"
	"testing"

	codex "github.com/freeeve/libcodex"
)

// subjectHeading finds the first 6xx data field whose $a matches label.
func subjectHeading(rec *codex.Record, tag, label string) (codex.Field, bool) {
	for _, f := range rec.DataFields(tag) {
		if f.SubfieldValue('a') == label {
			return f, true
		}
	}
	return codex.Field{}, false
}

// TestDecodeGrainMARCControlledSubjects proves SKOS-shaped controlled
// subjects reach MARC output: the emission writes bf:subject +
// skos:prefLabel, and the crosswalk (native since libcodex v0.15.0, which
// retired this repo's decode-local shim) renders them as
// `650 _7 $a Label $2 code $0 iri`, with the stored grain untouched.
func TestDecodeGrainMARCControlledSubjects(t *testing.T) {
	grain, _ := marcGrainFixture(t)
	const workID = "wmarc00000001"
	for _, subj := range []struct {
		uri, label, vocab string
	}{
		{"https://homosaurus.org/v4/homoit0000506", "Queer joy", "homosaurus"},
		{"http://id.worldcat.org/fast/1136767", "Substance abuse", "fast"},
		{"https://example.org/local/term1", "Zine culture", "local"},
	} {
		var err error
		grain, err = AppendAuthoritySubject(grain, workID, AuthoritySubject{
			URI: subj.uri, Labels: map[string]string{"en": subj.label, "es": subj.label + " (es)"},
		}, subj.vocab)
		if err != nil {
			t.Fatal(err)
		}
	}
	before := string(grain)
	recs, err := DecodeGrainMARC(grain)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 {
		t.Fatalf("decoded %d records", len(recs))
	}
	rec := recs[0]

	cases := []struct {
		label, sub2, sub0 string
		ind2              byte
	}{
		{"Queer joy", "homosaurus", "https://homosaurus.org/v4/homoit0000506", '7'},
		{"Substance abuse", "fast", "http://id.worldcat.org/fast/1136767", '7'},
		{"Zine culture", "", "https://example.org/local/term1", ' '}, // unknown authority: no $2
	}
	for _, c := range cases {
		f, ok := subjectHeading(rec, "650", c.label)
		if !ok {
			t.Errorf("no 650 for %q -- controlled subject vanished from MARC", c.label)
			continue
		}
		if f.Ind2 != c.ind2 {
			t.Errorf("%q: ind2 = %q, want %q", c.label, f.Ind2, c.ind2)
		}
		if got := f.SubfieldValue('2'); got != c.sub2 {
			t.Errorf("%q: $2 = %q, want %q", c.label, got, c.sub2)
		}
		if got := f.SubfieldValue('0'); got != c.sub0 {
			t.Errorf("%q: $0 = %q, want %q", c.label, got, c.sub0)
		}
	}
	// The Spanish prefLabel never becomes a second heading: one 650 per term,
	// English preferred.
	for _, f := range rec.DataFields("650") {
		if strings.HasSuffix(f.SubfieldValue('a'), "(es)") {
			t.Errorf("non-English prefLabel minted its own heading: %+v", f)
		}
	}
	// Nothing is decode-local state anymore, but the contract stands: the
	// grain bytes are unchanged.
	if string(grain) != before {
		t.Fatal("DecodeGrainMARC mutated the grain bytes")
	}
}
