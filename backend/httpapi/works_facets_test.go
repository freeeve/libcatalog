package httpapi

import (
	"encoding/json"
	"net/http"
	"net/url"
	"testing"

	"github.com/freeeve/libcodex/rdf"

	"github.com/freeeve/libcat/bibframe"
	"github.com/freeeve/libcat/ingest"
	"github.com/freeeve/libcat/storage/blob"
)

func sum(mod func(*ingest.WorkSummary)) ingest.WorkSummary {
	s := ingest.WorkSummary{
		WorkID: "w1", Title: "T", Contributors: []string{"A"},
		ISBNs: []string{"9780000000000"}, Subjects: []string{"http://x/s1"},
		Items: 1,
	}
	mod(&s)
	return s
}

func TestFacetBuckets(t *testing.T) {
	cases := []struct {
		name string
		mod  func(*ingest.WorkSummary)
		vis  string
		hold []string
		need []string
	}{
		{"complete public physical", func(s *ingest.WorkSummary) {}, "public", []string{"physical"}, nil},
		{"tombstoned beats suppressed", func(s *ingest.WorkSummary) { s.Tombstoned = true; s.Suppressed = true }, "tombstoned", []string{"physical"}, nil},
		{"suppressed", func(s *ingest.WorkSummary) { s.Suppressed = true }, "suppressed", []string{"physical"}, nil},
		{"withdrawn unkept", func(s *ingest.WorkSummary) { s.Withdrawn = "2026-07-01" }, "withdrawn", []string{"physical"}, nil},
		{"withdrawn kept is public", func(s *ingest.WorkSummary) { s.Withdrawn = "2026-07-01"; s.Kept = true }, "public", []string{"physical"}, nil},
		{"digital and physical", func(s *ingest.WorkSummary) { s.HasAvailability = true }, "public", []string{"physical", "digital"}, nil},
		{"no holdings", func(s *ingest.WorkSummary) { s.Items = 0 }, "public", []string{"none"}, nil},
		{"gaps", func(s *ingest.WorkSummary) { s.Subjects = nil; s.Contributors = nil; s.ISBNs = nil }, "public", []string{"physical"}, []string{"subjects", "contributors", "isbn"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := sum(tc.mod)
			if got := visibilityOf(s); got != tc.vis {
				t.Fatalf("visibilityOf = %q, want %q", got, tc.vis)
			}
			if got := holdingsOf(s); !equalStrings(got, tc.hold) {
				t.Fatalf("holdingsOf = %v, want %v", got, tc.hold)
			}
			if got := needsOf(s); !equalStrings(got, tc.need) {
				t.Fatalf("needsOf = %v, want %v", got, tc.need)
			}
		})
	}
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

// TestFacetSelfExclusion checks the defining facet property: a group's
// counts ignore its own filter but respect every other group's.
func TestFacetSelfExclusion(t *testing.T) {
	works := []ingest.WorkSummary{
		sum(func(s *ingest.WorkSummary) { s.WorkID = "w1" }),                                        // public, physical
		sum(func(s *ingest.WorkSummary) { s.WorkID = "w2"; s.Suppressed = true }),                   // suppressed, physical
		sum(func(s *ingest.WorkSummary) { s.WorkID = "w3"; s.Items = 0; s.HasAvailability = true }), // public, digital
	}
	f := parseWorkFilters(url.Values{"visibility": {"public"}})
	c := newFacetCounter()
	pass := 0
	for _, s := range works {
		m := f.groupMatches(s)
		c.add(s, m)
		ok := true
		for _, v := range m {
			ok = ok && v
		}
		if ok {
			pass++
		}
	}
	if pass != 2 {
		t.Fatalf("public filter passed %d works, want 2", pass)
	}
	got := c.result()
	// Visibility counts ignore the visibility filter: suppressed stays
	// countable so the user can flip to it.
	if !equalFacet(got["visibility"], []facetCount{{"public", 2}, {"suppressed", 1}}) {
		t.Fatalf("visibility counts = %v", got["visibility"])
	}
	// Holdings counts respect the visibility filter: only public works.
	if !equalFacet(got["holdings"], []facetCount{{"digital", 1}, {"physical", 1}}) {
		t.Fatalf("holdings counts = %v", got["holdings"])
	}
}

func equalFacet(a, b []facetCount) bool {
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

// seedFacetWork writes a grain carrying a controlled subject, an
// uncontrolled tag, and optionally the suppression marker.
func seedFacetWork(t *testing.T, bs blob.Store, workID, title, subjectIRI, tag string, suppressed bool) {
	t.Helper()
	const (
		bfNS    = "http://id.loc.gov/ontologies/bibframe/"
		rdfType = "http://www.w3.org/1999/02/22-rdf-syntax-ns#type"
	)
	ds := &rdf.Dataset{}
	feed := bibframe.FeedGraph("overdrive")
	work := rdf.NewIRI(bibframe.WorkIRI(workID))
	titleNode := rdf.NewIRI("#" + workID + "Title")
	ds.Add(work, rdf.NewIRI(rdfType), rdf.NewIRI(bfNS+"Work"), feed)
	ds.Add(work, rdf.NewIRI(bfNS+"title"), titleNode, feed)
	ds.Add(titleNode, rdf.NewIRI(bfNS+"mainTitle"), rdf.NewLiteral(title, "", ""), feed)
	if subjectIRI != "" {
		ds.Add(work, rdf.NewIRI(bfNS+"subject"), rdf.NewIRI(subjectIRI), feed)
	}
	if tag != "" {
		ds.Add(work, rdf.NewIRI(bibframe.PredTag), rdf.NewLiteral(tag, "", ""), feed)
	}
	if suppressed {
		ds.Add(work, rdf.NewIRI(bibframe.PredSuppressed), rdf.NewLiteral("true", "", ""), bibframe.EditorialGraph())
	}
	nq, err := ds.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bs.Put(t.Context(), bibframe.GrainPath(workID), nq, blob.PutOptions{}); err != nil {
		t.Fatal(err)
	}
}

// TestWorksListFacets drives the HTTP surface: filter params narrow the
// list and the response carries self-excluding counts.
func TestWorksListFacets(t *testing.T) {
	h, bs := newRecordsAPI(t)
	seedFacetWork(t, bs, "wsubj000001", "Subject Rich", "http://id.loc.gov/authorities/subjects/sh1", "space opera", false)
	seedFacetWork(t, bs, "wsubj000002", "Also Rich", "http://id.loc.gov/authorities/subjects/sh1", "", false)
	seedFacetWork(t, bs, "wbare000001", "Bare", "", "", false)
	seedFacetWork(t, bs, "whide000001", "Hidden", "", "", true)

	type facetsPage struct {
		worksPage
		Facets map[string][]facetCount `json:"facets"`
	}
	get := func(query string) facetsPage {
		t.Helper()
		rec := request(t, h, http.MethodGet, "/v1/works?"+query, "lib-token", "", nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /v1/works?%s = %d", query, rec.Code)
		}
		var page facetsPage
		if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
		return page
	}

	// Unfiltered: counts describe the catalog.
	page := get("")
	if page.Matched != 4 {
		t.Fatalf("matched = %d, want 4", page.Matched)
	}
	if !equalFacet(page.Facets["visibility"], []facetCount{{"public", 3}, {"suppressed", 1}}) {
		t.Fatalf("visibility = %v", page.Facets["visibility"])
	}
	if !equalFacet(page.Facets["subject"], []facetCount{{"http://id.loc.gov/authorities/subjects/sh1", 2}}) {
		t.Fatalf("subject = %v", page.Facets["subject"])
	}
	if !equalFacet(page.Facets["tag"], []facetCount{{"space opera", 1}}) {
		t.Fatalf("tag = %v", page.Facets["tag"])
	}

	// A subject filter narrows the works and the needs counts, but its own
	// group keeps the full picture.
	page = get("subject=" + url.QueryEscape("http://id.loc.gov/authorities/subjects/sh1"))
	if page.Matched != 2 || len(page.Works) != 2 {
		t.Fatalf("subject filter matched %d works %d", page.Matched, len(page.Works))
	}
	// needs counts now describe only the two subject-carrying works: both
	// have no ISBNs and no contributors, neither lacks subjects.
	for _, fc := range page.Facets["needs"] {
		if fc.Value == "subjects" {
			t.Fatalf("needs=subjects counted under subject filter: %v", page.Facets["needs"])
		}
	}

	// Needs and visibility filters compose (AND across groups).
	page = get("needs=subjects&visibility=suppressed")
	if page.Matched != 1 || page.Works[0].WorkID != "whide000001" {
		t.Fatalf("composed filters = %+v", page.Works)
	}

	// OR within a group.
	page = get("visibility=suppressed&visibility=public")
	if page.Matched != 4 {
		t.Fatalf("OR within visibility matched %d, want 4", page.Matched)
	}
}
