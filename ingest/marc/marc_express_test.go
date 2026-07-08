package marc

import (
	"context"
	"testing"

	"github.com/freeeve/libcat/ingest"
	codexbf "github.com/freeeve/libcodex/bibframe"
)

// TestMARCExpressReadSide is the MARC-import ramp's golden test (tasks/007): it runs
// OverDrive's real, vendored MARC Express sample records through the MARC provider and
// asserts the crosswalk recovers the three fields OverDrive actually uses --
// 037 $a (the Reserve ID), 084 $a (BISAC codes, $2 bisacsh), and 650 _7 $a (subjects,
// $2 OverDrive) -- proving the import path reads them correctly, not the placement the
// old synthetic writer inferred. The samples live under ingest/overdrive/testdata.
func TestMARCExpressReadSide(t *testing.T) {
	cases := []struct {
		file      string
		reserveID string   // 037 $a on the first record
		bisac     []string // 084 $a codes on the first record
		subject   string   // a 650 _7 $a subject on the first record
	}{
		{
			file:      "od-sample-ebook.mrc",
			reserveID: "D3938F63-D17C-4A4C-B6F4-0B17932AAB7C",
			bisac:     []string{"BUS027000", "HIS036060", "POL044000"},
			subject:   "Nonfiction.",
		},
		{
			file:      "od-sample-audiobook.mrc",
			reserveID: "930F9C3D-1C76-4C2A-B5A2-00F4D8336596",
			bisac:     []string{"FIC004000", "FIC028000", "FIC037000"},
			subject:   "Fiction.",
		},
	}

	for _, tc := range cases {
		t.Run(tc.file, func(t *testing.T) {
			prov, err := New(ingest.Config{Source: "../overdrive/testdata/marc-express/" + tc.file})
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			recs, err := prov.Records(context.Background())
			if err != nil {
				t.Fatalf("Records: %v", err)
			}
			if len(recs) != 15 {
				t.Fatalf("records = %d, want 15", len(recs))
			}

			// First record: golden values for each of the three fields.
			w0, i0 := recs[0].Work(), recs[0].Instance()
			if got := reserveID(i0); got != tc.reserveID {
				t.Errorf("037 reserve id = %q, want %q", got, tc.reserveID)
			}
			if got := bisacCodes(w0); !equalStrings(got, tc.bisac) {
				t.Errorf("084 BISAC = %v, want %v", got, tc.bisac)
			}
			if !hasSubject(w0, tc.subject, "OverDrive") {
				t.Errorf("650 _7 subjects %v missing %q ($2 OverDrive)", subjectLabels(w0), tc.subject)
			}

			// Every record in the file recovers all three field families -- the
			// crosswalk is consistent across the sample, not just record 0.
			for n, r := range recs {
				if reserveID(r.Instance()) == "" {
					t.Errorf("record %d: no 037 reserve id", n)
				}
				if len(bisacCodes(r.Work())) == 0 {
					t.Errorf("record %d: no 084 BISAC classification", n)
				}
				if countSubjects(r.Work(), "OverDrive") == 0 {
					t.Errorf("record %d: no 650 _7 $2 OverDrive subject", n)
				}
			}
		})
	}
}

// reserveID returns the OverDrive Reserve ID from the Instance: the non-ISBN
// bf:identifier the 037 $a crosswalks to (Class "Identifier"), or "".
func reserveID(inst codexbf.Instance) string {
	for _, id := range inst.Identifiers {
		if id.Class == "Identifier" && id.Value != "" {
			return id.Value
		}
	}
	return ""
}

// bisacCodes returns the Work's BISAC classification codes (084 $a, $2 bisacsh).
func bisacCodes(w codexbf.Work) []string {
	var out []string
	for _, c := range w.Classifications {
		if c.Source == "bisacsh" {
			out = append(out, c.Value)
		}
	}
	return out
}

// hasSubject reports whether the Work carries a subject with the given label and
// thesaurus source (650 _7 $a / $2).
func hasSubject(w codexbf.Work, label, source string) bool {
	for _, s := range w.Subjects {
		if s.Label == label && s.Source == source {
			return true
		}
	}
	return false
}

// countSubjects counts the Work's subjects with the given thesaurus source.
func countSubjects(w codexbf.Work, source string) int {
	n := 0
	for _, s := range w.Subjects {
		if s.Source == source {
			n++
		}
	}
	return n
}

func subjectLabels(w codexbf.Work) []string {
	out := make([]string, 0, len(w.Subjects))
	for _, s := range w.Subjects {
		out = append(out, s.Label)
	}
	return out
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
