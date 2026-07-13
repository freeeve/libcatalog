package vega

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/freeeve/libcat/ingest"
)

// Fixtures mirror live NYPL payloads (probed 2026-07-13): the suggestion
// list with highlight tags, the concept record with its explicit source,
// and a resources page with typed identifiers around the ISBNs.
const (
	suggBody = `[{"term":"Genderqueer <em>people</em>","id":"c-genderqueer","type":"Concept","roles":["subject"]},
{"term":"Genderqueer literature","id":"c-lit","type":"Concept","roles":["subject"]},
{"term":"Genderqueer people","id":"fg-somework","type":"FormatGroup","roles":[]}]`
	conceptHomoit = `{"id":"c-genderqueer","entityType":"Concept","type":"topic",
"marcKey":"650 7$aGenderqueer people.$2homoit","label":"Genderqueer people","source":"homoit"}`
	conceptLCSH = `{"id":"c-lcsh","entityType":"Concept","type":"topic","label":"Genderqueer people","source":"lcsh"}`
)

func resourcesBody(totalPages int, fgs ...string) string {
	return fmt.Sprintf(`{"totalPages":%d,"page":0,"totalResults":%d,"data":[%s]}`,
		totalPages, len(fgs), strings.Join(fgs, ","))
}

func fg(id, title string, isbns ...string) string {
	idents := []string{`{"type":"upc","value":"050837470200"}`, `{"type":"shelfMark","value":"306.76 D"}`}
	for _, i := range isbns {
		idents = append(idents, fmt.Sprintf(`{"type":"isbn","value":"%s"}`, i))
	}
	return fmt.Sprintf(`{"id":"%s","$title":["%s"],"identifiedBy":[%s]}`, id, title, strings.Join(idents, ","))
}

// tenantDoer serves canned bodies keyed by region + a path fragment, and
// records headers + request order per region.
type tenantDoer struct {
	mu      sync.Mutex
	pages   map[string]string // "<region>|<fragment>" matched by contains
	reqs    []string
	headers []http.Header
}

func (d *tenantDoer) Do(req *http.Request) (*http.Response, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	region := strings.TrimSuffix(req.URL.Hostname(), ".iiivega.com")
	d.reqs = append(d.reqs, region+"|"+req.URL.RequestURI())
	d.headers = append(d.headers, req.Header.Clone())
	for key, body := range d.pages {
		parts := strings.SplitN(key, "|", 2)
		if parts[0] == region && strings.Contains(req.URL.RequestURI(), parts[1]) {
			return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader([]byte(body)))}, nil
		}
	}
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("[]"))}, nil
}

func testTerms() []Term {
	return []Term{{
		URI:    "https://homosaurus.org/v5/homoit0000508",
		Labels: map[string]string{"en": "Genderqueer people"},
		Query:  "Genderqueer people",
	}}
}

// TestVegaChainEndToEnd pins the whole harvest chain on live payload
// shapes: suggestion filtering (subject-role Concepts only, highlight tags
// stripped, the LCSH twin gated out by the concept's source), resources
// paging, ISBN matching, and the endorsement carrying the tenant's
// verifiable attribution with a record link (task 458).
func TestVegaChainEndToEnd(t *testing.T) {
	doer := &tenantDoer{pages: map[string]string{
		"na2|/api/search/suggestions":  suggBody,
		"na2|/concepts/c-genderqueer":  conceptHomoit,
		"na2|/concepts/c-lit":          conceptLCSH,
		"na2|/resources/c-genderqueer": resourcesBody(1, fg("fg1", "Beyond They Them", "978-1-5248-9399-6", "1524893994")),
	}}
	works := []ingest.WorkSummary{
		{WorkID: "w1", Title: "Beyond They/Them", ISBNs: []string{"9781524893996"}},
		{WorkID: "w2", Title: "Unrelated", ISBNs: []string{"9780000000000"}},
	}
	e := New([]Tenant{{SiteCode: "nypl", Region: "na2"}}, testTerms(), WithClient(doer), WithDelay(0))
	got, err := e.Enrich(context.Background(), works)
	if err != nil {
		t.Fatalf("Enrich: %v", err)
	}
	if len(got) != 1 || got[0].WorkID != "w1" || got[0].Confidence != 0.9 {
		t.Fatalf("enrichments = %+v, want one ISBN-tier match on w1", got)
	}
	if got[0].Subjects[0].URI != "https://homosaurus.org/v5/homoit0000508" {
		t.Fatalf("subject = %+v, want the driver homoit URI", got[0].Subjects[0])
	}
	end := got[0].Endorsements[0]
	if end.Count != 1 || end.Sources[0] != "nypl.na2" {
		t.Fatalf("endorsement = %+v", end)
	}
	a := end.Attributions[0]
	if a.Basis != "isbn" || a.Key != "9781524893996" || !strings.Contains(a.Ref, "nypl.na2.iiivega.com/search/card?recordId=fg1") {
		t.Fatalf("attribution = %+v, want the isbn evidence and the record link", a)
	}

	// The handshake headers ride every request.
	h := doer.headers[0]
	if h.Get("iii-customer-domain") != "nypl.na2.iiivega.com" || h.Get("iii-host-domain") != "nypl.na2.iiivega.com" {
		t.Fatalf("handshake headers = %v", h)
	}
	if h.Get("Anonymous-User-Id") == "" {
		t.Fatal("missing Anonymous-User-Id")
	}
	// api-version: 1 for suggestions/resources, 2 for the concept read.
	for i, r := range doer.reqs {
		want := "1"
		if strings.Contains(r, "/concepts/") {
			want = "2"
		}
		if doer.headers[i].Get("api-version") != want {
			t.Fatalf("api-version on %s = %q, want %q", r, doer.headers[i].Get("api-version"), want)
		}
	}
}

// TestRegionSharedConceptResolution pins the update-2 optimization: two
// tenants in one region resolve each label ONCE (suggestion+concept reads
// are shared), and only the per-tenant resources reads repeat.
func TestRegionSharedConceptResolution(t *testing.T) {
	doer := &tenantDoer{pages: map[string]string{
		"na|/api/search/suggestions": strings.ReplaceAll(suggBody, "c-genderqueer", "c-shared"),
		"na|/concepts/c-shared":      strings.ReplaceAll(conceptHomoit, "c-genderqueer", "c-shared"),
		"na|/resources/c-shared":     resourcesBody(1, fg("fg1", "Shared", "9781111111111")),
	}}
	e := New([]Tenant{{SiteCode: "mdpls", Region: "na"}, {SiteCode: "mesa", Region: "na"}}, testTerms(), WithClient(doer), WithDelay(0))
	if _, err := e.Enrich(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	var sugg, concept, resources int
	for _, r := range doer.reqs {
		switch {
		case strings.Contains(r, "/suggestions"):
			sugg++
		case strings.Contains(r, "/concepts/"):
			concept++
		case strings.Contains(r, "/resources/"):
			resources++
		}
	}
	if sugg != 1 || concept != 1 {
		t.Fatalf("resolution requests = %d sugg + %d concept, want 1+1 shared across the region", sugg, concept)
	}
	if resources != 2 {
		t.Fatalf("resources requests = %d, want one per tenant", resources)
	}
	if st := e.RunStats(); st.Total != 2 || st.Batches != 2 {
		t.Fatalf("stats = %d/%d, want 2/2 (1 term x 2 tenants)", st.Batches, st.Total)
	}
}

// TestNoHomoitConceptMeansNoHarvest: a label whose only concept is LCSH
// resolves to nothing, cached, with zero resources reads.
func TestNoHomoitConceptMeansNoHarvest(t *testing.T) {
	doer := &tenantDoer{pages: map[string]string{
		"na2|/api/search/suggestions": `[{"term":"Genderqueer people","id":"c-lcsh","type":"Concept","roles":["subject"]}]`,
		"na2|/concepts/c-lcsh":        conceptLCSH,
	}}
	e := New([]Tenant{{SiteCode: "nypl", Region: "na2"}}, testTerms(), WithClient(doer), WithDelay(0))
	got, err := e.Enrich(context.Background(), []ingest.WorkSummary{{WorkID: "w1", ISBNs: []string{"9781524893996"}}})
	if err != nil || len(got) != 0 {
		t.Fatalf("got = %+v, %v; want nothing (LCSH concept gated out)", got, err)
	}
	for _, r := range doer.reqs {
		if strings.Contains(r, "/resources/") {
			t.Fatalf("resources fetched for a gated-out concept: %s", r)
		}
	}
}

// TestParseTenants pins the config form.
func TestParseTenants(t *testing.T) {
	ts, err := ParseTenants(" nypl.na2, mdpls.na ,")
	if err != nil || len(ts) != 2 || ts[0].Key() != "nypl.na2" || ts[1].Key() != "mdpls.na" {
		t.Fatalf("tenants = %+v, %v", ts, err)
	}
	if _, err := ParseTenants("justaslug"); err == nil {
		t.Fatal("a region-less tenant must refuse")
	}
}
