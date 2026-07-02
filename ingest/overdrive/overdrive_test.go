package overdrive

// sampleItem mirrors a real cached OverDrive audiobook record. It is the shared
// fixture for the package's tests (notably the direct BIBFRAME crosswalk in
// bibframe_test.go).
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
