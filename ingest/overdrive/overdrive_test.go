package overdrive

import (
	"path/filepath"
	"testing"
)

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
		Description: "<p><b>A stunning debut.</b></p><p>Herculine leaves the city&#8212;and her past.<br />What follows is&nbsp;unforgettable.</p>",
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

// TestReadCacheRejectsMissingOrEmptyDir covers a mistyped --cache
// path must error, not read as an empty feed.
func TestReadCacheRejectsMissingOrEmptyDir(t *testing.T) {
	if _, err := ReadCache(filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Error("missing cache dir should error")
	}
	if _, err := ReadCache(t.TempDir()); err == nil {
		t.Error("cache dir without page files should error")
	}
}
