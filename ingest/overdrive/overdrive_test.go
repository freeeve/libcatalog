package overdrive

import "testing"

// sampleItem mirrors a real cached OverDrive audiobook record.
func sampleItem() Item {
	return Item{
		ID:          "11682058",
		ReserveID:   "24760f5d-028e-4749-968f-85a458a79ad2",
		Title:       "Herculine",
		Subtitle:    "A Novel",
		Edition:     "Unabridged",
		Type:        NamedID{ID: "audiobook", Name: "Audiobook"},
		Publisher:   &NamedID{ID: "36805", Name: "Simon & Schuster Audio"},
		PublishDate: "2025-10-07T00:00:00Z",
		Creators: []Creator{
			{Name: "Grace Byron", Role: "Author", SortName: "Byron, Grace"},
			{Name: "Nicky Endres", Role: "Narrator", SortName: "Endres, Nicky"},
		},
		Languages: []NamedID{{ID: "en", Name: "English"}},
		Subjects:  []NamedID{{ID: "26", Name: "Fiction"}, {ID: "1224", Name: "LGBTQIA+ (Fiction)"}},
		BISAC:     []BISAC{{Code: "FIC073000", Description: "Fiction / LGBTQ+ / Transgender"}},
		Formats:   []Format{{Identifiers: []Identifier{{Type: "ISBN", Value: "9781668128251"}}}},
	}
}

func TestRecordCrosswalk(t *testing.T) {
	r := sampleItem().Record()

	// Leader/06 = 'i' (nonmusical sound recording) for an audiobook.
	if got := string(r.Leader()); got[6] != 'i' {
		t.Errorf("leader/06 = %q, want 'i' for audiobook", got[6])
	}

	checks := []struct {
		tag, code, want string
	}{
		{"001", "", "11682058"},
		{"020", "a", "9781668128251"},
		{"245", "a", "Herculine"},
		{"245", "b", "A Novel"},
		{"250", "a", "Unabridged"},
		{"100", "a", "Byron, Grace"},
		{"100", "4", "aut"},
		{"264", "b", "Simon & Schuster Audio"},
		{"264", "c", "2025"},
		{"041", "a", "eng"},
		{"072", "a", "FIC073000"},
	}
	for _, c := range checks {
		if c.code == "" {
			if got := r.ControlField(c.tag); got != c.want {
				t.Errorf("%s = %q, want %q", c.tag, got, c.want)
			}
			continue
		}
		if got := r.SubfieldValue(c.tag, c.code[0]); got != c.want {
			t.Errorf("%s $%s = %q, want %q", c.tag, c.code, got, c.want)
		}
	}

	// The narrator becomes a 700 with relator nrt.
	f700, ok := r.DataField("700")
	if !ok {
		t.Fatal("missing 700 for narrator")
	}
	if got := f700.SubfieldValue('a'); got != "Endres, Nicky" {
		t.Errorf("700 $a = %q, want narrator sort name", got)
	}
	if got := f700.SubfieldValue('4'); got != "nrt" {
		t.Errorf("700 $4 = %q, want nrt", got)
	}

	// Both subjects land as uncontrolled 653s.
	if got := len(r.DataFields("653")); got != 2 {
		t.Errorf("653 count = %d, want 2", got)
	}
}
