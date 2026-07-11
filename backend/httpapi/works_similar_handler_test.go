// GET /v1/works/{id}/similar -- the admin half of "more like this".
// The OPAC precomputes the same rail at build time; this scores it live off a
// cached index so an editor who has just re-subjected a work sees the neighbours
// move.
package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/freeeve/libcat/bibframe"
	"github.com/freeeve/libcat/similar"
	"github.com/freeeve/libcat/storage/blob"
	"github.com/freeeve/libcodex/rdf"

	"github.com/freeeve/libcat/backend/workindex"
)

// seedSubjectedWork writes a titled work carrying controlled subjects.
func seedSubjectedWork(t *testing.T, bs blob.Store, workID, title string, subjects ...string) {
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
	for _, s := range subjects {
		ds.Add(work, rdf.NewIRI(bfNS+"subject"), rdf.NewIRI(s), feed)
	}
	nq, err := ds.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bs.Put(t.Context(), bibframe.GrainPath(workID), nq, blob.PutOptions{}); err != nil {
		t.Fatal(err)
	}
}

type similarPage struct {
	Similar []similarNeighbor `json:"similar"`
}

func getSimilar(t *testing.T, h http.Handler, workID, query string) similarPage {
	t.Helper()
	url := "/v1/works/" + workID + "/similar"
	if query != "" {
		url += "?" + query
	}
	rec := request(t, h, http.MethodGet, url, "lib-token", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s = %d, want 200 (%s)", url, rec.Code, rec.Body.String())
	}
	var page similarPage
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	return page
}

func similarIDs(p similarPage) []string {
	out := make([]string, 0, len(p.Similar))
	for _, n := range p.Similar {
		out = append(out, n.WorkID)
	}
	return out
}

// pad gives the DF cap and the singleton floor something to bite on: with a
// handful of works every attribute looks rare.
func seedPadding(t *testing.T, bs blob.Store, n int) {
	t.Helper()
	for i := range n {
		seedSubjectedWork(t, bs, fmt.Sprintf("wpad%07d", i), fmt.Sprintf("Padding %d", i),
			fmt.Sprintf("https://ex.org/pad%d", i))
	}
}

// twoNeighbours: wa and wb share a subject; wc shares nothing.
func twoNeighbours(t *testing.T) (http.Handler, blob.Store) {
	t.Helper()
	h, bs := newRecordsAPI(t)
	seedPadding(t, bs, 20)
	seedSubjectedWork(t, bs, "wsim000001a", "Herculine", "https://ex.org/shared", "https://ex.org/only-a")
	seedSubjectedWork(t, bs, "wsim000001b", "The House of the Spirits", "https://ex.org/shared")
	seedSubjectedWork(t, bs, "wsim000001c", "A Lonely Book", "https://ex.org/nobody")
	return h, bs
}

func TestWorksSimilarReturnsNeighboursWithTitlesAndReasons(t *testing.T) {
	h, _ := twoNeighbours(t)

	page := getSimilar(t, h, "wsim000001a", "")
	if got := similarIDs(page); len(got) != 1 || got[0] != "wsim000001b" {
		t.Fatalf("similar = %v, want [wsim000001b]", got)
	}
	n := page.Similar[0]
	if n.Title != "The House of the Spirits" {
		t.Errorf("title = %q; the panel has nothing to render", n.Title)
	}
	if n.Score <= 0 {
		t.Errorf("score = %v, want > 0", n.Score)
	}
	// "Why is this here?" is the only question a cataloger asks about a
	// recommendation, and the answer is the subject they share -- not the one
	// only wa carries.
	if len(n.Shared) != 1 || n.Shared[0] != "https://ex.org/shared" {
		t.Errorf("shared = %v, want the one subject they share", n.Shared)
	}
}

// A work whose subjects nobody else carries scores nothing. That is an answer,
// not an error: 200 with an empty list.
func TestWorksSimilarWorkWithNoNeighboursIsEmptyNotMissing(t *testing.T) {
	h, _ := twoNeighbours(t)
	if page := getSimilar(t, h, "wsim000001c", ""); len(page.Similar) != 0 {
		t.Fatalf("similar = %v, want none", similarIDs(page))
	}
}

// A tombstoned work is excluded from the scorer, so it has no neighbours and is
// nobody's neighbour. It still exists, so it is 200-empty, not 404 --
// the two must stay distinguishable.
func TestWorksSimilarTombstonedIsNeitherNeighbourNorMissing(t *testing.T) {
	h, bs := newRecordsAPI(t)
	seedPadding(t, bs, 20)
	seedSubjectedWork(t, bs, "wsim000002a", "Live", "https://ex.org/shared")
	seedSubjectedWork(t, bs, "wsim000002d", "Retired", "https://ex.org/shared")
	seedTombstonedWork(t, bs, "wsim000002d", "Retired")

	if page := getSimilar(t, h, "wsim000002d", ""); len(page.Similar) != 0 {
		t.Errorf("a tombstoned work has neighbours: %v", similarIDs(page))
	}
	if page := getSimilar(t, h, "wsim000002a", ""); len(page.Similar) != 0 {
		t.Errorf("a tombstoned work was recommended: %v", similarIDs(page))
	}
	rec := request(t, h, http.MethodGet, "/v1/works/wsim000002d/similar", "lib-token", "", nil)
	if rec.Code != http.StatusOK {
		t.Errorf("tombstoned work = %d, want 200 (empty); 404 would say it does not exist", rec.Code)
	}
}

// A suppressed work is hidden from the public, not retired. The admin surface
// shows it, so it must be recommendable here even though the OPAC never sees it.
func TestWorksSimilarRecommendsSuppressedWorks(t *testing.T) {
	h, bs := newRecordsAPI(t)
	seedPadding(t, bs, 20)
	seedSubjectedWork(t, bs, "wsim000003a", "Live", "https://ex.org/shared")
	seedSubjectedWork(t, bs, "wsim000003s", "Suppressed", "https://ex.org/shared")
	grain, etag, err := bs.Get(t.Context(), bibframe.GrainPath("wsim000003s"))
	if err != nil {
		t.Fatal(err)
	}
	hidden, err := bibframe.SetSuppressed(grain, "wsim000003s", true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bs.Put(t.Context(), bibframe.GrainPath("wsim000003s"), hidden, blob.PutOptions{IfMatch: etag}); err != nil {
		t.Fatal(err)
	}
	if got := similarIDs(getSimilar(t, h, "wsim000003a", "")); len(got) != 1 || got[0] != "wsim000003s" {
		t.Fatalf("similar = %v, want the suppressed work", got)
	}
}

func TestWorksSimilarUnknownWorkIs404(t *testing.T) {
	h, _ := twoNeighbours(t)
	rec := request(t, h, http.MethodGet, "/v1/works/wzzzz00phantom/similar", "lib-token", "", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown work = %d, want 404", rec.Code)
	}
}

func TestWorksSimilarRejectsBadLimit(t *testing.T) {
	h, _ := twoNeighbours(t)
	for _, raw := range []string{"0", "-1", "51", "abc"} {
		rec := request(t, h, http.MethodGet, "/v1/works/wsim000001a/similar?limit="+raw, "lib-token", "", nil)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("limit=%s = %d, want 400", raw, rec.Code)
		}
	}
}

func TestWorksSimilarLimitCaps(t *testing.T) {
	h, bs := newRecordsAPI(t)
	seedPadding(t, bs, 20)
	for i := range 5 {
		seedSubjectedWork(t, bs, fmt.Sprintf("wsim00004%02d", i), fmt.Sprintf("Book %d", i), "https://ex.org/shared")
	}
	if got := getSimilar(t, h, "wsim0000400", "limit=2"); len(got.Similar) != 2 {
		t.Fatalf("limit=2 returned %d neighbours", len(got.Similar))
	}
	if got := getSimilar(t, h, "wsim0000400", ""); len(got.Similar) != 4 {
		t.Fatalf("default returned %d neighbours, want all 4", len(got.Similar))
	}
}

func TestWorksSimilarRequiresLibrarian(t *testing.T) {
	h, _ := twoNeighbours(t)
	rec := request(t, h, http.MethodGet, "/v1/works/wsim000001a/similar", "", "", nil)
	if rec.Code != http.StatusUnauthorized && rec.Code != http.StatusForbidden {
		t.Fatalf("anonymous = %d, want 401/403", rec.Code)
	}
}

// The cache is keyed on the work index's generation, which is the whole reason
// the admin half scores live instead of reading the OPAC's sidecar: an editor who
// has just re-subjected a work must see the neighbours move.
//
// Driven through workindex rather than the HTTP handler, because a grain written
// straight to the blob store is invisible to the index until a TTL refresh -- real
// edits go through Apply/Update, and so does this.
func TestSimilarIndexRebuildsWhenTheCorpusChanges(t *testing.T) {
	bs := blob.NewMem()
	seedPadding(t, bs, 20)
	seedSubjectedWork(t, bs, "wsim000005a", "Herculine", "https://ex.org/shared")
	seedSubjectedWork(t, bs, "wsim000005b", "Neighbour", "https://ex.org/shared")
	seedSubjectedWork(t, bs, "wsim000005c", "A Lonely Book", "https://ex.org/nobody")

	wix := workindex.New(bs, "data/works/")
	cache := &similarIndex{wix: wix, opts: similar.DefaultOptions}

	first, err := cache.get(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if got := first.ix.Neighbors("wsim000005a", 8); len(got) != 1 {
		t.Fatalf("before: %d neighbours, want 1", len(got))
	}

	// A second read with nothing changed must reuse the built index, not rebuild.
	again, err := cache.get(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if again != first {
		t.Error("the index was rebuilt though the corpus did not change")
	}

	// Re-subject the lonely book onto the shared heading, the way an editor does.
	seedSubjectedWork(t, bs, "wsim000005c", "A Lonely Book", "https://ex.org/shared")
	if err := wix.Update(t.Context(), bibframe.GrainPath("wsim000005c")); err != nil {
		t.Fatal(err)
	}

	after, err := cache.get(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if after == first {
		t.Fatal("the cached index survived a corpus change; the editor sees stale neighbours")
	}
	got := after.ix.Neighbors("wsim000005a", 8)
	if len(got) != 2 {
		t.Fatalf("after re-subjecting: %d neighbours, want 2", len(got))
	}
	if after.titles["wsim000005c"] != "A Lonely Book" {
		t.Errorf("titles went stale: %q", after.titles["wsim000005c"])
	}
}
