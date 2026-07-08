package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/freeeve/libcodex/rdf"

	"github.com/freeeve/libcat/bibframe"
	"github.com/freeeve/libcat/ingest"
	"github.com/freeeve/libcat/storage/blob"
)

// seedTitledWork writes a grain shaped the way SummarizeGrain expects: a
// typed bf:Work whose bf:title node carries the bf:mainTitle literal.
func seedTitledWork(t *testing.T, bs blob.Store, workID, title string) {
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
	nq, err := ds.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bs.Put(t.Context(), bibframe.GrainPath(workID), nq, blob.PutOptions{}); err != nil {
		t.Fatal(err)
	}
}

type worksPage struct {
	Works   []ingest.WorkSummary `json:"works"`
	Total   int                  `json:"total"`
	Matched int                  `json:"matched"`
	Offset  int                  `json:"offset"`
}

func listWorks(t *testing.T, h http.Handler, query string) worksPage {
	t.Helper()
	rec := request(t, h, http.MethodGet, "/v1/works?"+query, "lib-token", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /v1/works?%s = %d, want 200", query, rec.Code)
	}
	var page worksPage
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	return page
}

func TestWorksListPaging(t *testing.T) {
	h, bs := newRecordsAPI(t)
	for i := range 5 {
		seedTitledWork(t, bs, fmt.Sprintf("walpha%06d", i), fmt.Sprintf("Alpha Volume %d", i))
	}
	seedTitledWork(t, bs, "wbeta000000", "Beta Standalone")

	// The full list reports both counts.
	page := listWorks(t, h, "")
	if page.Total != 6 || page.Matched != 6 || len(page.Works) != 6 {
		t.Fatalf("unfiltered page = total %d matched %d works %d, want 6/6/6", page.Total, page.Matched, len(page.Works))
	}

	// A filtered window: matched counts every hit, works is the window.
	page = listWorks(t, h, "q=alpha&limit=2")
	if page.Matched != 5 || page.Total != 6 || len(page.Works) != 2 || page.Offset != 0 {
		t.Fatalf("first window = total %d matched %d works %d offset %d, want 6/5/2/0", page.Total, page.Matched, len(page.Works), page.Offset)
	}

	// The next window continues where the first ended.
	page = listWorks(t, h, "q=alpha&limit=2&offset=4")
	if page.Matched != 5 || len(page.Works) != 1 || page.Offset != 4 {
		t.Fatalf("last window = matched %d works %d offset %d, want 5/1/4", page.Matched, len(page.Works), page.Offset)
	}

	// An offset past the end is empty, not an error.
	page = listWorks(t, h, "q=alpha&offset=50")
	if page.Matched != 5 || len(page.Works) != 0 {
		t.Fatalf("past-the-end window = matched %d works %d, want 5/0", page.Matched, len(page.Works))
	}
}
