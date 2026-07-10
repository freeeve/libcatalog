// tasks/280: the admin work search listed tombstoned records alongside live
// ones, undifferentiated. On the demo playground that meant 49 retired e2e
// sentinels and one real book. A tombstone says "this record is retired"; a
// cataloger searching for a book is not looking for one.
package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/freeeve/libcat/bibframe"
	"github.com/freeeve/libcat/storage/blob"
)

// seedTombstonedWork writes a titled work and retires it, the way
// POST /v1/works/{id}/visibility does.
func seedTombstonedWork(t *testing.T, bs blob.Store, workID, title string) {
	t.Helper()
	seedTitledWork(t, bs, workID, title)
	grain, etag, err := bs.Get(t.Context(), bibframe.GrainPath(workID))
	if err != nil {
		t.Fatal(err)
	}
	retired, err := bibframe.SetTombstone(grain, workID, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bs.Put(t.Context(), bibframe.GrainPath(workID), retired, blob.PutOptions{IfMatch: etag}); err != nil {
		t.Fatal(err)
	}
}

// oneLiveAmongRetired is the playground's own shape: a pile of retired records
// and one live book.
func oneLiveAmongRetired(t *testing.T) http.Handler {
	t.Helper()
	h, bs := newRecordsAPI(t)
	seedTitledWork(t, bs, "wlive000001", "Gideon the Ninth")
	for _, id := range []string{"wdead000001", "wdead000002", "wdead000003"} {
		seedTombstonedWork(t, bs, id, "Retired "+id)
	}
	return h
}

func titlesOf(page worksPage) []string {
	out := make([]string, 0, len(page.Works))
	for _, w := range page.Works {
		out = append(out, w.Title)
	}
	return out
}

// The default. A cataloger opening work search must not have to wade through
// records the catalog has retired.
func TestWorksListExcludesTombstonedByDefault(t *testing.T) {
	h := oneLiveAmongRetired(t)

	page := listWorks(t, h, "")
	if len(page.Works) != 1 || page.Works[0].WorkID != "wlive000001" {
		t.Fatalf("works = %v, want only the live one", titlesOf(page))
	}
	if page.Total != 1 || page.Matched != 1 {
		t.Fatalf("total %d matched %d, want 1/1: the counts must describe the set that was searched", page.Total, page.Matched)
	}
}

// Retired records stay reachable: un-retiring one, checking a redirect and
// auditing a merge all need to find it.
func TestWorksListIncludesTombstonedWhenAsked(t *testing.T) {
	h := oneLiveAmongRetired(t)

	page := listWorks(t, h, "tombstoned=include")
	if len(page.Works) != 4 || page.Total != 4 || page.Matched != 4 {
		t.Fatalf("include = %d works, total %d matched %d, want 4/4/4", len(page.Works), page.Total, page.Matched)
	}
	var retired int
	for _, w := range page.Works {
		if w.Tombstoned {
			retired++
		}
	}
	if retired != 3 {
		t.Fatalf("tombstoned rows = %d, want 3: the client cannot mark what it cannot see", retired)
	}
}

// "what did I retire?" is the audit question, and the withdrawal queue answers
// it badly.
func TestWorksListOnlyTombstoned(t *testing.T) {
	h := oneLiveAmongRetired(t)

	page := listWorks(t, h, "tombstoned=only")
	if len(page.Works) != 3 || page.Total != 3 || page.Matched != 3 {
		t.Fatalf("only = %d works, total %d matched %d, want 3/3/3", len(page.Works), page.Total, page.Matched)
	}
	for _, w := range page.Works {
		if !w.Tombstoned {
			t.Fatalf("a live work leaked into tombstoned=only: %s", w.WorkID)
		}
	}
}

// The filter runs before the query and before paging, so a window never
// promises more pages than it has. This is the reason it is not a client-side
// filter: "matched 4" with one row is the bug that shape produces.
func TestWorksListTombstonedFilterRunsBeforePaging(t *testing.T) {
	h, bs := newRecordsAPI(t)
	seedTitledWork(t, bs, "wlive000001", "Alpha Live")
	for _, id := range []string{"wdead000001", "wdead000002", "wdead000003"} {
		seedTombstonedWork(t, bs, id, "Alpha Retired")
	}

	page := listWorks(t, h, "q=alpha&limit=2")
	if page.Matched != 1 || len(page.Works) != 1 {
		t.Fatalf("matched %d works %d, want 1/1: the retired rows must not be counted as hits", page.Matched, len(page.Works))
	}
	if page.Total != 1 {
		t.Fatalf("total = %d, want 1: total must describe the same set the query searched", page.Total)
	}
}

// The facet rail must describe the set on screen. With tombstoned records
// excluded, there is no tombstoned bucket to offer -- selecting it would return
// nothing, which is a worse answer than not offering it.
func TestWorksListFacetsDescribeTheVisibleSet(t *testing.T) {
	h := oneLiveAmongRetired(t)

	if got := visibilityCounts(t, h, ""); got["tombstoned"] != 0 {
		t.Fatalf("default visibility facet offers a tombstoned bucket of %d, want none", got["tombstoned"])
	}
	if got := visibilityCounts(t, h, ""); got["public"] != 1 {
		t.Fatalf("default visibility facet public = %d, want 1", got["public"])
	}
	got := visibilityCounts(t, h, "tombstoned=include")
	if got["tombstoned"] != 3 || got["public"] != 1 {
		t.Fatalf("include visibility facet = %v, want 3 tombstoned and 1 public", got)
	}
}

// An unrecognized mode is refused. Silently falling back to the default would
// show a client that asked for "only" an empty list, and it would conclude the
// records were gone.
func TestWorksListRejectsAnUnknownTombstonedMode(t *testing.T) {
	h := oneLiveAmongRetired(t)

	rec := request(t, h, http.MethodGet, "/v1/works?tombstoned=yes", "lib-token", "", nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("tombstoned=yes = %d, want 400", rec.Code)
	}
}

// An extras facet named "tombstoned" must not shadow the parameter.
func TestTombstonedIsAReservedWorkParam(t *testing.T) {
	if !reservedWorkParams["tombstoned"] {
		t.Fatal("an extras facet could shadow ?tombstoned= and swallow the filter")
	}
}

// visibilityCounts reads the visibility facet group's buckets.
func visibilityCounts(t *testing.T, h http.Handler, query string) map[string]int {
	t.Helper()
	rec := request(t, h, http.MethodGet, "/v1/works?"+query, "lib-token", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /v1/works?%s = %d", query, rec.Code)
	}
	var page struct {
		Facets map[string][]struct {
			Value string `json:"value"`
			Count int    `json:"count"`
		} `json:"facets"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	out := map[string]int{}
	for _, v := range page.Facets["visibility"] {
		out[v.Value] = v.Count
	}
	return out
}
