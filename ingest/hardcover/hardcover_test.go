package hardcover_test

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/freeeve/libcat/ingest"
	"github.com/freeeve/libcat/ingest/hardcover"
	"github.com/freeeve/libcat/project"
)

const fixture = "testdata/read-shelf.json"

// TestRecordsExplodeByFormat proves a captured shelf explodes into one record per
// collapsed edition format, with per-format instance keys and the shared reading-log
// extras carried on each (tasks/026).
func TestRecordsExplodeByFormat(t *testing.T) {
	prov, err := hardcover.New(ingest.Config{Source: fixture})
	if err != nil {
		t.Fatal(err)
	}
	recs, err := prov.Records(context.Background())
	if err != nil {
		t.Fatalf("Records: %v", err)
	}
	// Herculine -> ebook + audiobook (2), Left Hand -> physical (1), Ambient -> ebook (1).
	if len(recs) != 4 {
		t.Fatalf("records = %d, want 4", len(recs))
	}

	// The two Herculine records share a Work-clustering identity (author/title/lang) but
	// carry distinct instance keys, and both expose the same extras.
	var herculine []ingest.Record
	for _, r := range recs {
		if r.Work().Titles[0].MainTitle == "Herculine" {
			herculine = append(herculine, r)
		}
	}
	if len(herculine) != 2 {
		t.Fatalf("Herculine records = %d, want 2", len(herculine))
	}
	a, b := herculine[0].Identity(), herculine[1].Identity()
	if a.Author != b.Author || a.Title != b.Title || a.Lang != b.Lang {
		t.Errorf("Herculine format records must share cluster fields: %+v vs %+v", a, b)
	}
	if a.Author != "Byron, Grace" {
		t.Errorf("author = %q, want %q", a.Author, "Byron, Grace")
	}
	if reflect.DeepEqual(a.ProviderKeys, b.ProviderKeys) {
		t.Errorf("Herculine formats must have distinct instance keys, both = %v", a.ProviderKeys)
	}
	ep, ok := herculine[0].(ingest.ExtraProvider)
	if !ok {
		t.Fatal("record does not implement ingest.ExtraProvider")
	}
	wantExtra := map[string]string{
		"cover":    "https://covers.example.org/herculine.jpg",
		"rating":   "5",
		"dateRead": "2026-01-15",
	}
	if got := ep.Extras(); !reflect.DeepEqual(got, wantExtra) {
		t.Errorf("extras = %v, want %v", got, wantExtra)
	}
}

// TestEndToEndProjection runs the fixture through the real ingest.Run -> project path
// and asserts the catalog matches the demo's pipeline: clustered formats, normalized
// contributors, genre tags (from both object and string cached_tags), and the
// cover/rating/dateRead extras (tasks/026). No network.
func TestEndToEndProjection(t *testing.T) {
	prov, err := hardcover.New(ingest.Config{Source: fixture})
	if err != nil {
		t.Fatal(err)
	}
	out := t.TempDir()
	if _, err := ingest.Run(prov, out); err != nil {
		t.Fatalf("Run: %v", err)
	}
	nq, err := os.ReadFile(filepath.Join(out, "catalog.nq"))
	if err != nil {
		t.Fatal(err)
	}
	cat, err := project.Project(nq, "hardcover")
	if err != nil {
		t.Fatalf("Project: %v", err)
	}
	if len(cat.Works) != 3 {
		t.Fatalf("works = %d, want 3", len(cat.Works))
	}
	byTitle := map[string]project.Work{}
	for _, w := range cat.Works {
		byTitle[w.Title] = w
	}

	// Herculine: an ebook and an audiobook edition cluster into one Work with both
	// formats and two instances; primary author leads; genres become tags; extras ride.
	h := byTitle["Herculine"]
	if !reflect.DeepEqual(h.Formats, []string{"audiobook", "ebook"}) {
		t.Errorf("Herculine formats = %v, want [audiobook ebook]", h.Formats)
	}
	if len(h.Instances) != 2 {
		t.Errorf("Herculine instances = %d, want 2", len(h.Instances))
	}
	wantContribs := []project.Contributor{
		{Name: "Byron, Grace", Role: "author"},
		{Name: "Endres, Nicky", Role: "narrator"},
	}
	if !reflect.DeepEqual(h.Contributors, wantContribs) {
		t.Errorf("Herculine contributors = %v, want %v", h.Contributors, wantContribs)
	}
	// Fiction/Horror/LGBTQ all map to controlled subjects, so they leave tags[] and
	// become subjects with authority labels + skos:broader (tasks/026).
	if len(h.Tags) != 0 {
		t.Errorf("Herculine tags = %v, want none (all promoted to subjects)", h.Tags)
	}
	gotSubjectIDs := map[string]project.Subject{}
	for _, s := range h.Subjects {
		gotSubjectIDs[s.ID] = s
	}
	for _, wantID := range []string{
		"https://homosaurus.org/v3/homoit0000827",
		"https://id.loc.gov/authorities/subjects/sh2026001126",
		"https://id.loc.gov/authorities/subjects/sh85062084",
	} {
		if _, ok := gotSubjectIDs[wantID]; !ok {
			t.Errorf("Herculine missing controlled subject %s; got %v", wantID, h.Subjects)
		}
	}
	if lg := gotSubjectIDs["https://homosaurus.org/v3/homoit0000827"]; lg.Labels["en"] != "LGBTQ books" {
		t.Errorf("LGBTQ subject label = %q, want %q", lg.Labels["en"], "LGBTQ books")
	}
	if horror := gotSubjectIDs["https://id.loc.gov/authorities/subjects/sh85062084"]; !reflect.DeepEqual(horror.Broader, []string{"https://id.loc.gov/authorities/subjects/sh2026001126"}) {
		t.Errorf("Horror broader = %v, want [Fiction]", horror.Broader)
	}
	wantExtra := map[string]string{
		"cover":    "https://covers.example.org/herculine.jpg",
		"rating":   "5",
		"dateRead": "2026-01-15",
	}
	if !reflect.DeepEqual(h.Extra, wantExtra) {
		t.Errorf("Herculine extra = %v, want %v", h.Extra, wantExtra)
	}
	// The description projects as first-class bf:summary, not an extra (tasks/124).
	if h.Summary != "A haunting debut novel." {
		t.Errorf("Herculine summary = %q, want %q", h.Summary, "A haunting debut novel.")
	}
	if !hasHardcoverProvenance(h) {
		t.Errorf("Herculine instances missing a hardcover source-tagged id: %+v", h.Instances)
	}

	// Left Hand: physical edition -> "print"; comma-bearing name passes through; the
	// genre came from a string-wrapped cached_tags value; rating keeps its half star.
	l := byTitle["The Left Hand of Darkness"]
	if !reflect.DeepEqual(l.Formats, []string{"print"}) {
		t.Errorf("Left Hand formats = %v, want [print]", l.Formats)
	}
	if len(l.Contributors) != 1 || l.Contributors[0].Name != "Le Guin, Ursula K." {
		t.Errorf("Left Hand contributors = %v, want [{Le Guin, Ursula K. author}]", l.Contributors)
	}
	// "Science Fiction" (from the string-wrapped cached_tags) maps to a controlled
	// subject with a broader parent, so it leaves tags[].
	if len(l.Tags) != 0 {
		t.Errorf("Left Hand tags = %v, want none (Science Fiction promoted)", l.Tags)
	}
	if len(l.Subjects) != 1 || l.Subjects[0].ID != "https://id.loc.gov/authorities/subjects/sh85118629" {
		t.Fatalf("Left Hand subjects = %v, want [sh85118629]", l.Subjects)
	}
	if l.Subjects[0].Labels["en"] != "Science fiction" {
		t.Errorf("Science fiction label = %q, want %q", l.Subjects[0].Labels["en"], "Science fiction")
	}
	if !reflect.DeepEqual(l.Subjects[0].Broader, []string{"https://id.loc.gov/authorities/subjects/sh2026001126"}) {
		t.Errorf("Science fiction broader = %v, want [Fiction]", l.Subjects[0].Broader)
	}
	if l.Extra["rating"] != "4.5" || l.Extra["dateRead"] != "2025-06-01" {
		t.Errorf("Left Hand extra = %v, want rating 4.5 / dateRead 2025-06-01", l.Extra)
	}

	// Ambient: text-format fallback (Kindle) -> ebook; no contributors; no extras.
	a := byTitle["Ambient Novel"]
	if !reflect.DeepEqual(a.Formats, []string{"ebook"}) {
		t.Errorf("Ambient formats = %v, want [ebook] (edition_format fallback)", a.Formats)
	}
	if len(a.Contributors) != 0 {
		t.Errorf("Ambient contributors = %v, want none", a.Contributors)
	}
	if a.Extra != nil {
		t.Errorf("Ambient extra = %v, want nil", a.Extra)
	}
}

// TestLiveShapeFixture runs a real captured shelf (testdata/read-shelf-live.json, 3 books
// with editions trimmed to a representative set per format) through the pipeline and
// asserts structural properties, so a Hardcover schema drift that breaks the crosswalk is
// caught even though the synthetic fixture above pins the exact edge-case behavior.
func TestLiveShapeFixture(t *testing.T) {
	prov, err := hardcover.New(ingest.Config{Source: "testdata/read-shelf-live.json"})
	if err != nil {
		t.Fatal(err)
	}
	out := t.TempDir()
	if _, err := ingest.Run(prov, out); err != nil {
		t.Fatalf("Run: %v", err)
	}
	nq, err := os.ReadFile(filepath.Join(out, "catalog.nq"))
	if err != nil {
		t.Fatal(err)
	}
	cat, err := project.Project(nq, "hardcover")
	if err != nil {
		t.Fatalf("Project: %v", err)
	}
	if len(cat.Works) != 3 {
		t.Fatalf("works = %d, want 3", len(cat.Works))
	}
	byTitle := map[string]project.Work{}
	labeledBroaderSubjects := 0
	for _, w := range cat.Works {
		byTitle[w.Title] = w
		if len(w.Formats) == 0 || len(w.Instances) == 0 {
			t.Errorf("%q: formats/instances empty (%v / %d)", w.Title, w.Formats, len(w.Instances))
		}
		if len(w.Contributors) == 0 {
			t.Errorf("%q: no contributors", w.Title)
		}
		if e := w.Extra; e == nil || e["cover"] == "" || e["rating"] == "" || e["dateRead"] == "" {
			t.Errorf("%q: incomplete extras: %v", w.Title, e)
		}
		for _, s := range w.Subjects {
			if s.Labels["en"] != "" && len(s.Broader) > 0 {
				labeledBroaderSubjects++
			}
		}
	}
	// The Martian clusters ebook/audiobook/print editions and credits Andy Weir.
	m, ok := byTitle["The Martian"]
	if !ok {
		t.Fatalf("The Martian missing from %v", byTitle)
	}
	if len(m.Formats) < 2 {
		t.Errorf("The Martian formats = %v, want the multi-format cluster", m.Formats)
	}
	if m.Contributors[0].Name != "Weir, Andy" {
		t.Errorf("The Martian primary contributor = %q, want %q", m.Contributors[0].Name, "Weir, Andy")
	}
	// At least one controlled subject resolved a localized label and a broader parent,
	// exercising the authority table against real genres.
	if labeledBroaderSubjects == 0 {
		t.Error("no controlled subject with both a label and a broader parent in the live fixture")
	}
}

// hasHardcoverProvenance reports whether every instance carries a hardcover-source id.
func hasHardcoverProvenance(w project.Work) bool {
	for _, inst := range w.Instances {
		found := false
		for _, pid := range inst.ProviderIDs {
			if pid.Source == hardcover.SourceHardcover && pid.Value != "" {
				found = true
			}
		}
		if !found {
			return false
		}
	}
	return len(w.Instances) > 0
}
