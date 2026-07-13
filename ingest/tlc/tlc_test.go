package tlc

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/freeeve/libcat/ingest"
)

// Fixtures mirror the live NBPL payload (probed 2026-07-13): totalHits +
// resources with typed standardNumbers around the ISBNs.
func searchBody(totalHits int, recs ...string) string {
	return fmt.Sprintf(`{"totalHits":%d,"facetFilters":[],"resources":[%s]}`, totalHits, strings.Join(recs, ","))
}

func rec(id int, title string, isbns ...string) string {
	sns := []string{`{"id":1,"type":"Lccn","data":"2024944182"}`}
	for i, n := range isbns {
		sns = append(sns, fmt.Sprintf(`{"id":%d,"type":"Isbn","data":"%s"}`, 10+i, n))
	}
	return fmt.Sprintf(`{"id":%d,"shortTitle":"%s","shortAuthor":"De Robertis, Caro","standardNumbers":[%s]}`,
		id, title, strings.Join(sns, ","))
}

// tenantDoer serves canned bodies keyed by "host|label|startIndex" and
// records the request bodies + headers, goroutine-safe.
type tenantDoer struct {
	mu     sync.Mutex
	pages  map[string]string
	bodies []map[string]any
	hdrs   []http.Header
	hosts  []string
}

func (d *tenantDoer) Do(req *http.Request) (*http.Response, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	raw, _ := io.ReadAll(req.Body)
	var body map[string]any
	_ = json.Unmarshal(raw, &body)
	d.bodies = append(d.bodies, body)
	d.hdrs = append(d.hdrs, req.Header.Clone())
	host := strings.TrimSuffix(req.URL.Hostname(), ".tlcdelivers.com")
	d.hosts = append(d.hosts, host)
	key := fmt.Sprintf("%s|%v|%v", host, body["searchTerm"], body["startIndex"])
	if page, ok := d.pages[key]; ok {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(page))}, nil
	}
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(searchBody(0)))}, nil
}

func testTerms() []Term {
	return []Term{{
		URI:    "https://homosaurus.org/v5/homoit0001509",
		Labels: map[string]string{"en": "Trans people"},
		Query:  "Trans people",
	}}
}

// TestTLCChain pins the harvest on the live payload shape: the strict
// faceted request body, ISBN matching, the endorsement attribution, and the
// handshake headers (task 459).
func TestTLCChain(t *testing.T) {
	doer := &tenantDoer{pages: map[string]string{
		"nbpl|Trans people|0": searchBody(1, rec(133613150, "So Many Stars", "9781643756905", "1643756877")),
	}}
	works := []ingest.WorkSummary{
		{WorkID: "w1", Title: "So Many Stars", ISBNs: []string{"978-1-64375-690-5"}},
		{WorkID: "w2", Title: "Unrelated", ISBNs: []string{"9780000000000"}},
	}
	e := New([]string{"nbpl"}, testTerms(), WithClient(doer), WithDelay(0))
	got, err := e.Enrich(context.Background(), works)
	if err != nil {
		t.Fatalf("Enrich: %v", err)
	}
	if len(got) != 1 || got[0].WorkID != "w1" || got[0].Confidence != 0.9 {
		t.Fatalf("enrichments = %+v", got)
	}
	end := got[0].Endorsements[0]
	if end.Count != 1 || end.Sources[0] != "nbpl" || end.Attributions[0].Key != "9781643756905" {
		t.Fatalf("endorsement = %+v", end)
	}

	// The strict request schema: term doubles as the Subject facet.
	b := doer.bodies[0]
	if b["searchTerm"] != "Trans people" {
		t.Fatalf("searchTerm = %v", b["searchTerm"])
	}
	ff := b["facetFilters"].([]any)[0].(map[string]any)
	if ff["facetName"] != "Subject" || ff["facetValue"] != "Trans people" {
		t.Fatalf("facet = %v", ff)
	}
	h := doer.hdrs[0]
	if h.Get("Ls2pac-config-name") != "pac" || h.Get("Ls2pac-config-type") != "pac" {
		t.Fatalf("handshake headers = %v", h)
	}
}

// TestTLCPaginationByTotalHits stops exactly when totalHits are covered and
// truncates at the page cap on terms that keep going.
func TestTLCPaginationByTotalHits(t *testing.T) {
	full := searchBody(40, rec(1, "A", "9781111111111"), rec(2, "B", "9782222222222"))
	doer := &tenantDoer{pages: map[string]string{
		"nbpl|Trans people|0": full, "nbpl|Trans people|2": full, "nbpl|Trans people|4": full,
	}}
	e := New([]string{"nbpl"}, testTerms(), WithClient(doer), WithDelay(0), WithMaxPages(3))
	e.hitsPerPage = 2
	if _, err := e.Enrich(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if len(doer.bodies) != 3 {
		t.Fatalf("requests = %d, want the page cap of 3 (totalHits 40 never covered)", len(doer.bodies))
	}

	// A short set stops on its own: totalHits 2 at page size 2 = one page.
	doer2 := &tenantDoer{pages: map[string]string{
		"nbpl|Trans people|0": searchBody(2, rec(1, "A", "9781111111111"), rec(2, "B", "9782222222222")),
	}}
	e2 := New([]string{"nbpl"}, testTerms(), WithClient(doer2), WithDelay(0))
	e2.hitsPerPage = 2
	if _, err := e2.Enrich(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if len(doer2.bodies) != 1 {
		t.Fatalf("requests = %d, want 1 (totalHits covered)", len(doer2.bodies))
	}
}

// nullDoer answers the contract-drift shape: totalHits null.
type nullDoer struct{}

func (nullDoer) Do(*http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"totalHits":null,"resources":null}`))}, nil
}

// TestTLCNullTotalHitsFailsLoudly: a null totalHits means the strict
// request schema was rejected -- a skipped term with the skip counted, not
// a silent empty harvest.
func TestTLCNullTotalHitsFailsLoudly(t *testing.T) {
	e := New([]string{"nbpl"}, testTerms(), WithClient(nullDoer{}), WithDelay(0))
	got, err := e.Enrich(context.Background(), []ingest.WorkSummary{{WorkID: "w1", ISBNs: []string{"9781643756905"}}})
	if err != nil || len(got) != 0 {
		t.Fatalf("got = %+v, %v", got, err)
	}
	if st := e.RunStats(); st.SkippedBatches != 1 {
		t.Fatalf("skipped = %d, want the schema rejection counted", st.SkippedBatches)
	}
}

// TestTLCMultiTenantConsensus: two tenants matching one pair endorse a
// single suggestion; Total = terms x hosts.
func TestTLCMultiTenantConsensus(t *testing.T) {
	page := searchBody(1, rec(7, "So Many Stars", "9781643756905"))
	doer := &tenantDoer{pages: map[string]string{
		"nbpl|Trans people|0": page,
		"zzpl|Trans people|0": page,
	}}
	works := []ingest.WorkSummary{{WorkID: "w1", ISBNs: []string{"9781643756905"}}}
	e := New([]string{"nbpl", "zzpl"}, testTerms(), WithClient(doer), WithDelay(0))
	got, err := e.Enrich(context.Background(), works)
	if err != nil || len(got) != 1 {
		t.Fatalf("got = %+v, %v", got, err)
	}
	end := got[0].Endorsements[0]
	if end.Count != 2 || strings.Join(end.Sources, ",") != "nbpl,zzpl" {
		t.Fatalf("endorsement = %+v, want both tenants", end)
	}
	if st := e.RunStats(); st.Total != 2 || st.Batches != 2 || st.Candidates != 1 {
		t.Fatalf("stats = %+v", st)
	}
}
