package diversity

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// TestSeedIsWellFormed is the golden guard on the shipped crosswalk: every category
// has an id, a label, and at least one keyword, and ids are unique.
func TestSeedIsWellFormed(t *testing.T) {
	cw := Default()
	cats := cw.Categories()
	if len(cats) == 0 {
		t.Fatal("seed crosswalk has no categories")
	}
	seen := map[string]bool{}
	for _, c := range cats {
		if c.ID == "" || c.Label == "" {
			t.Errorf("category %+v missing id or label", c)
		}
		if seen[c.ID] {
			t.Errorf("duplicate category id %q", c.ID)
		}
		seen[c.ID] = true
		if len(cw.keywords[c.ID]) == 0 {
			t.Errorf("category %q has no keywords", c.ID)
		}
	}
}

// TestCategorizeByKeyword covers the common ILS case: bare heading strings with no
// authority URI are matched by whole-word/phrase keywords, and a work can land in
// more than one category.
func TestCategorizeByKeyword(t *testing.T) {
	cw := Default()
	cases := []struct {
		label string
		want  []string
	}{
		{"LGBTQIA+ (Fiction)", []string{"lgbtqia"}},
		{"African American women", []string{"women-gender", "bipoc"}},
		{"Literature", nil},
		{"Fiction", nil},
		{"Immigrants--United States", []string{"immigrant-diaspora"}},
		{"Autism in children", []string{"disability-neurodiversity"}},
	}
	for _, tc := range cases {
		got := cw.Categorize("", tc.label, "")
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("Categorize(%q) = %v, want %v", tc.label, got, tc.want)
		}
	}
}

// TestKeywordWordBoundary guards against substring false positives: a keyword must
// match as a whole word or phrase, not as a fragment of a longer word.
func TestKeywordWordBoundary(t *testing.T) {
	cw := Default()
	if got := cw.Categorize("", "Gaya (India)", ""); got != nil {
		t.Errorf("'gay' must not match inside 'Gaya': got %v", got)
	}
	if got := cw.Categorize("", "Poore, Benjamin Perley", ""); got != nil {
		t.Errorf("'poor' must not match inside 'Poore': got %v", got)
	}
	if got := cw.Categorize("", "Gay pride parades", ""); !reflect.DeepEqual(got, []string{"lgbtqia"}) {
		t.Errorf("'gay' should match the whole word: got %v", got)
	}
}

// TestCategorizeByURI covers the controlled case: an exact authority-URI match,
// seeded here via an override so the test does not depend on specific seed URIs.
func TestCategorizeByURI(t *testing.T) {
	dir := t.TempDir()
	over := filepath.Join(dir, "over.toml")
	writeFile(t, over, `
[[category]]
id = "lgbtqia"
uris = ["http://id.loc.gov/authorities/subjects/sh2007003123"]
`)
	cw, err := Load(over)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// Exact URI hits even when the label carries no keyword.
	got := cw.Categorize("http://id.loc.gov/authorities/subjects/sh2007003123", "Some neutral heading", "")
	if !reflect.DeepEqual(got, []string{"lgbtqia"}) {
		t.Errorf("URI match = %v, want [lgbtqia]", got)
	}
	// A different URI with no keyword hit maps nowhere.
	if got := cw.Categorize("http://id.loc.gov/authorities/subjects/sh0000000000", "Neutral", ""); got != nil {
		t.Errorf("unmapped URI = %v, want nil", got)
	}
}

// TestOverrideMergesAndAdds checks the seed-plus-override contract: an existing
// category unions new keywords, a non-empty label replaces, and a new id is added in
// stable order after the seed categories.
func TestOverrideMergesAndAdds(t *testing.T) {
	dir := t.TempDir()
	over := filepath.Join(dir, "over.toml")
	writeFile(t, over, `
[[category]]
id = "lgbtqia"
label = "LGBTQIA+ (local)"
keywords = ["achillean"]

[[category]]
id = "veterans"
label = "Veterans"
keywords = ["veterans", "military families"]
`)
	cw, err := Load(over)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// Union: the seed keyword still matches and the added one now matches too.
	if got := cw.Categorize("", "Lesbian poets", ""); !reflect.DeepEqual(got, []string{"lgbtqia"}) {
		t.Errorf("seed keyword lost after override: got %v", got)
	}
	if got := cw.Categorize("", "Achillean love", ""); !reflect.DeepEqual(got, []string{"lgbtqia"}) {
		t.Errorf("override keyword not added: got %v", got)
	}
	// Label replaced.
	if got := cw.Label("lgbtqia"); got != "LGBTQIA+ (local)" {
		t.Errorf("label = %q, want the override label", got)
	}
	// New category appended after the seed categories.
	cats := cw.Categories()
	if last := cats[len(cats)-1]; last.ID != "veterans" {
		t.Errorf("added category should sort last: got %q", last.ID)
	}
	if got := cw.Categorize("", "Military families", ""); !reflect.DeepEqual(got, []string{"veterans"}) {
		t.Errorf("new category not matched: got %v", got)
	}
}

// TestCategorizeSubjectsRollup checks the work-level roll-up: multiple subjects
// dedupe to a stable, seed-ordered set of category ids.
func TestCategorizeSubjectsRollup(t *testing.T) {
	cw := Default()
	subs := []SubjectRef{
		{Labels: []string{"LGBTQIA+ (Fiction)"}},
		{Labels: []string{"Immigrants"}},
		{Labels: []string{"Gay men"}}, // same category as the first -> deduped
		{Labels: []string{"Cooking"}}, // no match
	}
	got := cw.CategorizeSubjects(subs)
	want := []string{"lgbtqia", "immigrant-diaspora"} // seed order: lgbtqia before immigrant
	if !reflect.DeepEqual(got, want) {
		t.Errorf("CategorizeSubjects = %v, want %v", got, want)
	}
}

// TestPluralTolerantKeywords is the inflection fix: a heading's plural
// matches a singular seed keyword, one-directionally and shallowly, and the old
// substring false-positive guards still hold.
func TestPluralTolerantKeywords(t *testing.T) {
	cw := Default()
	cases := []struct {
		label string
		want  []string
	}{
		{"Lesbians", []string{"lgbtqia"}},             // 7,287 uses in the QLL review, missed before
		{"Drag queens", []string{"lgbtqia"}},          // plural on the phrase's last word
		{"Transsexual people", []string{"lgbtqia"}},   // new seed vocabulary
		{"Genderfluid people", []string{"lgbtqia"}},   // new seed vocabulary
		{"Homophobia in sports", []string{"lgbtqia"}}, // -phobia terms
		{"Sapphics", []string{"lgbtqia"}},             // plural of a new keyword
		{"Homosexuality", []string{"lgbtqia"}},        // FAST noun-form heading, 3,377 occ in queerbooks
		{"Bisexuality--Fiction", []string{"lgbtqia"}}, // -ity class, subdivided heading
		{"Lesbianism", []string{"lgbtqia"}},           // -ism noun form
		{"Gaya (India)", nil},                         // still no substring match
		{"Poore, Benjamin Perley", nil},               // "poors"/"poores" != "poore"
		{"Transactions of the Royal Society", nil},    // "trans" must not match "transactions"
	}
	for _, tc := range cases {
		if got := cw.Categorize("", tc.label, ""); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("Categorize(%q) = %v, want %v", tc.label, got, tc.want)
		}
	}
}

// TestSeedIntersectionalURIs: the Homosaurus intersectional identity terms map
// to their racial/ethnic/indigenous/religious category AND to lgbtqia via the
// scheme -- one subject, both dimensions.
func TestSeedIntersectionalURIs(t *testing.T) {
	cw := Default()
	got := cw.Categorize("https://homosaurus.org/v5/homoit0000208", "Black LGBTQ+ people", "homosaurus")
	if !reflect.DeepEqual(got, []string{"bipoc", "lgbtqia"}) {
		t.Errorf("Black LGBTQ+ people = %v, want [bipoc lgbtqia]", got)
	}
	got = cw.Categorize("https://homosaurus.org/v5/homoit0001480", "Two-Spirit people", "homosaurus")
	if !reflect.DeepEqual(got, []string{"indigenous", "lgbtqia"}) {
		t.Errorf("Two-Spirit people = %v, want [indigenous lgbtqia]", got)
	}
	got = cw.Categorize("https://homosaurus.org/v5/homoit0000682", "Jewish LGBTQ+ people", "homosaurus")
	if !reflect.DeepEqual(got, []string{"lgbtqia", "religious-minorities"}) {
		t.Errorf("Jewish LGBTQ+ people = %v, want [lgbtqia religious-minorities]", got)
	}
}

// TestSchemeMatching is the Homosaurus ask: a subject in a
// category-relevant vocabulary counts by scheme code alone, whatever its label,
// and overrides can add schemes to a category.
func TestSchemeMatching(t *testing.T) {
	cw := Default()
	// A Homosaurus term whose label carries no seed keyword still counts.
	got := cw.Categorize("https://homosaurus.org/v3/homoit0000674", "Chosen family", "homosaurus")
	if !reflect.DeepEqual(got, []string{"lgbtqia"}) {
		t.Errorf("homosaurus scheme = %v, want [lgbtqia]", got)
	}
	// Scheme codes compare case-insensitively; unrelated schemes map nowhere.
	if got := cw.Categorize("", "Neutral", "Homosaurus"); !reflect.DeepEqual(got, []string{"lgbtqia"}) {
		t.Errorf("scheme match should be case-insensitive: %v", got)
	}
	if got := cw.Categorize("http://id.worldcat.org/fast/1", "Neutral", "fast"); got != nil {
		t.Errorf("fast scheme maps nowhere by default: %v", got)
	}

	// An override can attach a scheme to its own category.
	over := filepath.Join(t.TempDir(), "over.toml")
	writeFile(t, over, `
[[category]]
id = "indigenous"
schemes = ["nautil"]
`)
	cw2, err := Load(over)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cw2.Categorize("", "Anything", "nautil"); !reflect.DeepEqual(got, []string{"indigenous"}) {
		t.Errorf("override scheme = %v, want [indigenous]", got)
	}
}

// TestLoadMissingOverrideErrors checks that a configured-but-unreadable override is
// a hard error, not a silent fallback to the seed.
func TestLoadMissingOverrideErrors(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "does-not-exist.toml")); err == nil {
		t.Fatal("Load with a missing override should error")
	}
	// An empty path is skipped, not an error.
	if _, err := Load(""); err != nil {
		t.Fatalf("empty override path should be skipped: %v", err)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// TestReligionCategoryRescope: the representation frame admits
// denomination-level faith-community terms; the id keeps the historical slug
// so operator overrides keyed on it keep unioning.
func TestReligionCategoryRescope(t *testing.T) {
	cw := Default()
	if got := cw.Label("religious-minorities"); got != "Religion & faith communities" {
		t.Errorf("label = %q, want the representation framing", got)
	}
	for _, uri := range []string{
		"https://homosaurus.org/v5/homoit0000048", // LGBTQ+ Anglicans
		"https://homosaurus.org/v5/homoit0001069", // LGBTQ+ Pagans
		"https://homosaurus.org/v5/homoit0000386", // LGBTQ+ Eastern Orthodox Christians
	} {
		got := cw.Categorize(uri, "", "homosaurus")
		if !reflect.DeepEqual(got, []string{"lgbtqia", "religious-minorities"}) {
			t.Errorf("Categorize(%s) = %v, want [lgbtqia religious-minorities]", uri, got)
		}
	}
	if got := cw.Categorize("", "Wiccans", ""); !reflect.DeepEqual(got, []string{"religious-minorities"}) {
		t.Errorf("Wiccans = %v, want the faith-communities category", got)
	}
}
