package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/freeeve/libcodex/rdf"

	"github.com/freeeve/libcat/bibframe"
	"github.com/freeeve/libcat/storage/blob"
)

// seedBareWorkGrain adds a titled work with no instances, enough to be a
// relation endpoint.
func seedBareWorkGrain(t *testing.T, bs blob.Store, workID, title string) {
	t.Helper()
	ds := &rdf.Dataset{}
	feed := bibframe.FeedGraph("overdrive")
	bf := "http://id.loc.gov/ontologies/bibframe/"
	work := rdf.NewIRI(bibframe.WorkIRI(workID))
	ds.Add(work, rdf.NewIRI("http://www.w3.org/1999/02/22-rdf-syntax-ns#type"), rdf.NewIRI(bf+"Work"), feed)
	titleNode := rdf.NewBlank("t0")
	ds.Add(work, rdf.NewIRI(bf+"title"), titleNode, feed)
	ds.Add(titleNode, rdf.NewIRI(bf+"mainTitle"), rdf.NewLiteral(title, "", ""), feed)
	nq, err := ds.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bs.Put(t.Context(), bibframe.GrainPath(workID), nq, blob.PutOptions{}); err != nil {
		t.Fatal(err)
	}
}

// TestWorkRelationsAPI covers tasks/221: linking writes both directions,
// listing resolves titles, unlinking retracts both sides, and phantom /
// self links refuse before anything is written.
func TestWorkRelationsAPI(t *testing.T) {
	h, bs := newRecordsAPI(t)
	seedWorkGrain(t, bs)                  // editWorkID, "A Book"
	seedISBNGrain(t, bs, "9781250313195") // isbnWorkID
	link := map[string]string{"kind": "hasPart", "target": isbnWorkID}

	// Self-link and phantom target refuse.
	if rec := request(t, h, http.MethodPost, "/v1/works/"+editWorkID+"/relations", "lib-token", "", map[string]string{"kind": "hasPart", "target": editWorkID}); rec.Code != http.StatusBadRequest {
		t.Fatalf("self link = %d", rec.Code)
	}
	if rec := request(t, h, http.MethodPost, "/v1/works/"+editWorkID+"/relations", "lib-token", "", map[string]string{"kind": "hasPart", "target": "wzzzz00phantom"}); rec.Code != http.StatusNotFound {
		t.Fatalf("phantom target = %d", rec.Code)
	}
	if rec := request(t, h, http.MethodPost, "/v1/works/"+editWorkID+"/relations", "lib-token", "", map[string]string{"kind": "sideways", "target": isbnWorkID}); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad kind = %d", rec.Code)
	}

	// Link, then both sides list it (the inverse on the target).
	if rec := request(t, h, http.MethodPost, "/v1/works/"+editWorkID+"/relations", "lib-token", "", link); rec.Code != http.StatusNoContent {
		t.Fatalf("link = %d %s", rec.Code, rec.Body)
	}
	var got struct{ HasPart, PartOf []relationEntry }
	rec := request(t, h, http.MethodGet, "/v1/works/"+editWorkID+"/relations", "lib-token", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list = %d", rec.Code)
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.HasPart) != 1 || got.HasPart[0].WorkID != isbnWorkID || len(got.PartOf) != 0 {
		t.Fatalf("source relations = %+v", got)
	}
	if got.HasPart[0].Title != "Companion Volume" {
		t.Fatalf("target title should resolve from the index: %+v", got.HasPart[0])
	}
	rec = request(t, h, http.MethodGet, "/v1/works/"+isbnWorkID+"/relations", "lib-token", "", nil)
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.PartOf) != 1 || got.PartOf[0].WorkID != editWorkID {
		t.Fatalf("target relations = %+v", got)
	}

	// Unlink retracts both sides.
	if rec := request(t, h, http.MethodDelete, "/v1/works/"+editWorkID+"/relations", "lib-token", "", link); rec.Code != http.StatusNoContent {
		t.Fatalf("unlink = %d %s", rec.Code, rec.Body)
	}
	for _, id := range []string{editWorkID, isbnWorkID} {
		rec = request(t, h, http.MethodGet, "/v1/works/"+id+"/relations", "lib-token", "", nil)
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if len(got.HasPart)+len(got.PartOf) != 0 {
			t.Fatalf("%s still related after unlink: %+v", id, got)
		}
	}

	// Anonymous refuses.
	if rec := request(t, h, http.MethodPost, "/v1/works/"+editWorkID+"/relations", "", "", link); rec.Code != http.StatusUnauthorized {
		t.Fatalf("anon link = %d", rec.Code)
	}
}

// TestWorkRelationCycles covers tasks/232: an add that would make a work both
// contain and be contained by another refuses before either side is written,
// as does a longer cycle through the hasPart walk -- while a diamond (two
// routes to one part) and an idempotent re-add stay allowed.
func TestWorkRelationCycles(t *testing.T) {
	h, bs := newRecordsAPI(t)
	const cWorkID = "wccc123ccc456"
	seedWorkGrain(t, bs)                  // A
	seedISBNGrain(t, bs, "9781250313195") // B
	seedBareWorkGrain(t, bs, cWorkID, "Third Volume")

	relate := func(method, workID, kind, target string) int {
		return request(t, h, method, "/v1/works/"+workID+"/relations", "lib-token", "", map[string]string{"kind": kind, "target": target}).Code
	}
	relationsOf := func(workID string) (got struct{ HasPart, PartOf []relationEntry }) {
		rec := request(t, h, http.MethodGet, "/v1/works/"+workID+"/relations", "lib-token", "", nil)
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		return got
	}
	ids := func(entries []relationEntry) []string {
		out := make([]string, 0, len(entries))
		for _, e := range entries {
			out = append(out, e.WorkID)
		}
		return out
	}

	if code := relate(http.MethodPost, editWorkID, "hasPart", isbnWorkID); code != http.StatusNoContent {
		t.Fatalf("A hasPart B = %d", code)
	}
	// The contradiction, stated from either end.
	if code := relate(http.MethodPost, editWorkID, "partOf", isbnWorkID); code != http.StatusBadRequest {
		t.Fatalf("A partOf B over an existing A hasPart B = %d, want 400", code)
	}
	if code := relate(http.MethodPost, isbnWorkID, "hasPart", editWorkID); code != http.StatusBadRequest {
		t.Fatalf("B hasPart A over an existing A hasPart B = %d, want 400", code)
	}
	// Neither refusal left a statement on either grain, and the original link
	// survives untouched.
	a, b := relationsOf(editWorkID), relationsOf(isbnWorkID)
	if len(a.HasPart) != 1 || a.HasPart[0].WorkID != isbnWorkID || len(a.PartOf) != 0 {
		t.Fatalf("A = %v / %v after refusals", ids(a.HasPart), ids(a.PartOf))
	}
	if len(b.PartOf) != 1 || b.PartOf[0].WorkID != editWorkID || len(b.HasPart) != 0 {
		t.Fatalf("B = %v / %v after refusals", ids(b.HasPart), ids(b.PartOf))
	}
	// Re-adding the link it already holds is still idempotent, not a cycle.
	if code := relate(http.MethodPost, editWorkID, "hasPart", isbnWorkID); code != http.StatusNoContent {
		t.Fatalf("idempotent re-add = %d", code)
	}
	// Removing a link that was never written stays a no-op.
	if code := relate(http.MethodDelete, editWorkID, "partOf", isbnWorkID); code != http.StatusNoContent {
		t.Fatalf("remove of an absent link = %d", code)
	}

	// A -> B -> C: closing C -> A is a three-work cycle the walk must find.
	if code := relate(http.MethodPost, isbnWorkID, "hasPart", cWorkID); code != http.StatusNoContent {
		t.Fatalf("B hasPart C = %d", code)
	}
	if code := relate(http.MethodPost, cWorkID, "hasPart", editWorkID); code != http.StatusBadRequest {
		t.Fatalf("C hasPart A closing A->B->C = %d, want 400", code)
	}
	// But a diamond is not a cycle: A may contain C directly as well.
	if code := relate(http.MethodPost, cWorkID, "partOf", editWorkID); code != http.StatusNoContent {
		t.Fatalf("A hasPart C alongside A->B->C = %d, want 204", code)
	}
	if c := relationsOf(cWorkID); len(c.PartOf) != 2 || len(c.HasPart) != 0 {
		t.Fatalf("C = %v / %v, want two parents and no parts", ids(c.HasPart), ids(c.PartOf))
	}
}
