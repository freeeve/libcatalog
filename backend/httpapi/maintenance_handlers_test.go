package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/freeeve/libcodex/rdf"

	"github.com/freeeve/libcat/bibframe"
	"github.com/freeeve/libcat/project"
	"github.com/freeeve/libcat/storage/blob"

	"github.com/freeeve/libcat/backend/auth"
	"github.com/freeeve/libcat/backend/store"
)

func newMaintenanceAPI(t *testing.T) (http.Handler, blob.Store) {
	t.Helper()
	bs := blob.NewMem()
	seedTypedWork(t, bs, "wvis00000001", nil, "")
	verifier := staffVerifier{"lib-token": {Email: "lib@example.org", Roles: []auth.Role{auth.RoleLibrarian}}}
	return New(Deps{Blob: bs, DB: store.NewMem(), Verifier: verifier}), bs
}

// TestWithdrawnQueue is the review surface: a reconciliation-
// flagged work appears in the queue; "suppress" hides it (leaving the flag
// as the reason) and "keep" clears the flag with a sticky keep decision.
func TestWithdrawnQueue(t *testing.T) {
	h, bs := newMaintenanceAPI(t)
	const workID = "wvis00000001"

	// Flag the seeded work the way a reconcile pass would.
	grain, _, _ := bs.Get(t.Context(), bibframe.GrainPath(workID))
	grain, err := bibframe.SetWithdrawn(grain, workID, "2026-07-03")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bs.Put(t.Context(), bibframe.GrainPath(workID), grain, blob.PutOptions{}); err != nil {
		t.Fatal(err)
	}

	rec := request(t, h, http.MethodGet, "/v1/withdrawn", "lib-token", "", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), workID) {
		t.Fatalf("queue = %d %s", rec.Code, rec.Body.String())
	}

	// Suppress: hidden, flag stays, queue empties.
	rec = request(t, h, http.MethodPost, "/v1/works/"+workID+"/withdrawn", "lib-token", "",
		map[string]string{"action": "suppress"})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"suppressed":true`) ||
		!strings.Contains(rec.Body.String(), `"withdrawn":"2026-07-03"`) {
		t.Fatalf("suppress = %d %s", rec.Code, rec.Body.String())
	}
	rec = request(t, h, http.MethodGet, "/v1/withdrawn", "lib-token", "", nil)
	if strings.Contains(rec.Body.String(), workID) {
		t.Fatalf("suppressed row still queued: %s", rec.Body.String())
	}

	// Keep: unsuppress first (the curator changed their mind), then keep --
	// the flag clears and the decision sticks.
	request(t, h, http.MethodPost, "/v1/works/"+workID+"/visibility", "lib-token", "",
		map[string]string{"action": "unsuppress"})
	rec = request(t, h, http.MethodPost, "/v1/works/"+workID+"/withdrawn", "lib-token", "",
		map[string]string{"action": "keep"})
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), `"withdrawn"`) ||
		!strings.Contains(rec.Body.String(), `"kept":true`) {
		t.Fatalf("keep = %d %s", rec.Code, rec.Body.String())
	}

	if rec := request(t, h, http.MethodPost, "/v1/works/"+workID+"/withdrawn", "lib-token", "",
		map[string]string{"action": "purge"}); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad action = %d", rec.Code)
	}
}

func TestVisibilityFlow(t *testing.T) {
	h, bs := newMaintenanceAPI(t)
	const workID = "wvis00000001"

	// Suppress hides from projection without a redirect.
	rec := request(t, h, http.MethodPost, "/v1/works/"+workID+"/visibility", "lib-token", "",
		map[string]string{"action": "suppress"})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"suppressed":true`) {
		t.Fatalf("suppress = %d %s", rec.Code, rec.Body.String())
	}
	grain, _, _ := bs.Get(t.Context(), bibframe.GrainPath(workID))
	cat, err := project.Project(grain, "overdrive")
	if err != nil || len(cat.Works) != 0 {
		t.Fatalf("suppressed still projects: %v works, %v", len(cat.Works), err)
	}
	rm, _ := project.Redirects(grain)
	if len(rm.Redirects) != 0 {
		t.Fatalf("suppress redirected: %+v", rm.Redirects)
	}

	// Unsuppress restores; tombstone with a successor leaves a redirect.
	request(t, h, http.MethodPost, "/v1/works/"+workID+"/visibility", "lib-token", "",
		map[string]string{"action": "unsuppress"})
	rec = request(t, h, http.MethodPost, "/v1/works/"+workID+"/visibility", "lib-token", "",
		map[string]string{"action": "tombstone", "redirectTo": "wsucc0000001"})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"tombstoned":true`) {
		t.Fatalf("tombstone = %d %s", rec.Code, rec.Body.String())
	}
	grain, _, _ = bs.Get(t.Context(), bibframe.GrainPath(workID))
	cat, _ = project.Project(grain, "overdrive")
	if len(cat.Works) != 0 {
		t.Fatal("tombstoned still projects")
	}
	rm, _ = project.Redirects(grain)
	if len(rm.Redirects) != 1 || rm.Redirects[0].From != workID || rm.Redirects[0].To != "wsucc0000001" {
		t.Fatalf("redirects = %+v", rm.Redirects)
	}

	// State reads back; untombstone restores.
	rec = request(t, h, http.MethodGet, "/v1/works/"+workID+"/visibility", "lib-token", "", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"redirectTo":"wsucc0000001"`) {
		t.Fatalf("get visibility = %d %s", rec.Code, rec.Body.String())
	}
	request(t, h, http.MethodPost, "/v1/works/"+workID+"/visibility", "lib-token", "",
		map[string]string{"action": "untombstone"})
	grain, _, _ = bs.Get(t.Context(), bibframe.GrainPath(workID))
	cat, _ = project.Project(grain, "overdrive")
	if len(cat.Works) != 1 {
		t.Fatal("untombstone did not restore")
	}
}

// seedBarcodeWork writes a work grain carrying one instance with one item that
// holds the given barcode -- the minimal shape the duplicate-barcode report scans.
func seedBarcodeWork(t *testing.T, bs blob.Store, workID, barcode string) {
	t.Helper()
	const bfNS = "http://id.loc.gov/ontologies/bibframe/"
	ds := &rdf.Dataset{}
	feed := bibframe.FeedGraph("overdrive")
	ed := bibframe.EditorialGraph()
	work := rdf.NewIRI(bibframe.WorkIRI(workID))
	inst := rdf.NewIRI(bibframe.InstanceIRI(workID + "i"))
	ds.Add(work, rdf.NewIRI("http://www.w3.org/1999/02/22-rdf-syntax-ns#type"), rdf.NewIRI(bfNS+"Work"), feed)
	ds.Add(inst, rdf.NewIRI(bfNS+"instanceOf"), work, feed)
	item := rdf.NewBlank("it0")
	ds.Add(inst, rdf.NewIRI(bfNS+"hasItem"), item, ed)
	ds.Add(item, rdf.NewIRI(bibframe.PredBarcode), rdf.NewLiteral(barcode, "", ""), ed)
	nq, err := ds.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bs.Put(t.Context(), bibframe.GrainPath(workID), nq, blob.PutOptions{}); err != nil {
		t.Fatal(err)
	}
}

// TestDuplicateBarcodesReport is the report route: a barcode held by
// more than one item across the corpus is surfaced with the works holding it.
func TestDuplicateBarcodesReport(t *testing.T) {
	bs := blob.NewMem()
	seedBarcodeWork(t, bs, "wdup00000001", "SHARED-BC")
	seedBarcodeWork(t, bs, "wdup00000002", "SHARED-BC") // same barcode
	seedBarcodeWork(t, bs, "wuniq0000003", "SOLO-BC")
	verifier := staffVerifier{"lib-token": {Email: "lib@example.org", Roles: []auth.Role{auth.RoleLibrarian}}}
	h := New(Deps{Blob: bs, DB: store.NewMem(), Verifier: verifier})

	rec := request(t, h, http.MethodGet, "/v1/items/duplicate-barcodes", "lib-token", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("report = %d %s", rec.Code, rec.Body)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "SHARED-BC") || strings.Contains(body, "SOLO-BC") {
		t.Fatalf("report should list SHARED-BC only, got %s", body)
	}
	if !strings.Contains(body, "wdup00000001") || !strings.Contains(body, "wdup00000002") {
		t.Fatalf("report should name both holders, got %s", body)
	}
}

// TestItemPutRejectsBarcodeHeldByAnother is the constraint: a PUT that
// assigns a barcode already held by a live item on a different instance is
// refused (409); re-saving the instance's own barcode is not a collision.
func TestItemPutRejectsBarcodeHeldByAnother(t *testing.T) {
	bs := blob.NewMem()
	seedBarcodeWork(t, bs, "wone00000001", "SHARED-BC")
	seedBarcodeWork(t, bs, "wtwo00000002", "OWN-BC")
	verifier := staffVerifier{"lib-token": {Email: "lib@example.org", Roles: []auth.Role{auth.RoleLibrarian}}}
	h := New(Deps{Blob: bs, DB: store.NewMem(), Verifier: verifier})

	rec := request(t, h, http.MethodGet, "/v1/works/wtwo00000002/items", "lib-token", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("get items = %d %s", rec.Code, rec.Body)
	}
	var got struct {
		Etag string `json:"etag"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &got)

	// Assigning SHARED-BC (held by wone's item) to wtwo's instance -> 409.
	body := map[string]any{"instanceId": "wtwo00000002i", "items": []map[string]string{{"barcode": "SHARED-BC"}}}
	rec = request(t, h, http.MethodPut, "/v1/works/wtwo00000002/items", "lib-token", got.Etag, body)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "wone00000001") {
		t.Fatalf("put with a taken barcode = %d %s, want 409 naming wone", rec.Code, rec.Body)
	}

	// Control: re-saving wtwo's own barcode on its own instance is not a collision.
	body2 := map[string]any{"instanceId": "wtwo00000002i", "items": []map[string]string{{"barcode": "OWN-BC"}}}
	rec = request(t, h, http.MethodPut, "/v1/works/wtwo00000002/items", "lib-token", got.Etag, body2)
	if rec.Code != http.StatusOK {
		t.Fatalf("re-saving the instance's own barcode = %d %s, want 200", rec.Code, rec.Body)
	}
}

// TestTombstoneRejectsSelfRedirect pins the guard: a tombstone whose
// redirectTo is the work being tombstoned would republish a permalink that loops
// (the successor IS the retired page), so it is refused -- symmetric to the
// relations (target != work) and merge (from != to) self-guards. A tombstone to a
// different successor still works.
func TestTombstoneRejectsSelfRedirect(t *testing.T) {
	h, _ := newMaintenanceAPI(t)
	const workID = "wvis00000001" // the seeded work grain

	rec := request(t, h, http.MethodPost, "/v1/works/"+workID+"/visibility", "lib-token", "",
		map[string]string{"action": "tombstone", "redirectTo": workID})
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "redirect to itself") {
		t.Fatalf("self-redirect tombstone = %d %s, want 400 refusing a self-redirect", rec.Code, rec.Body.String())
	}

	// Control: a tombstone to a different successor is accepted, so the guard is
	// specific to the self case and did not simply break tombstoning.
	if rec := request(t, h, http.MethodPost, "/v1/works/"+workID+"/visibility", "lib-token", "",
		map[string]string{"action": "tombstone", "redirectTo": "wsucc0000001"}); rec.Code != http.StatusOK {
		t.Fatalf("tombstone to a distinct successor = %d %s", rec.Code, rec.Body.String())
	}
}

// TestItemsRoundTrip is the acceptance: bf:Item fields round-trip
// grain -> editor -> projection.
func TestItemsRoundTrip(t *testing.T) {
	h, bs := newMaintenanceAPI(t)
	const workID = "wvis00000001"

	// The seeded work has no instances; give it one via the grain directly.
	grain, _, _ := bs.Get(t.Context(), bibframe.GrainPath(workID))
	withInst := string(grain) +
		`<#wvis00000001Work> <http://id.loc.gov/ontologies/bibframe/hasInstance> <#ivis00000001Instance> <feed:overdrive> .` + "\n" +
		`<#ivis00000001Instance> <http://www.w3.org/1999/02/22-rdf-syntax-ns#type> <http://id.loc.gov/ontologies/bibframe/Instance> <feed:overdrive> .` + "\n"
	if _, err := bs.Put(t.Context(), bibframe.GrainPath(workID), []byte(withInst), blob.PutOptions{}); err != nil {
		t.Fatal(err)
	}

	// The items PUT is a client-token PUT: read the list, edit it,
	// write it back under the token the read handed out.
	_, etag := getItems(t, h)
	rec := request(t, h, http.MethodPut, "/v1/works/"+workID+"/items", "lib-token", etag, map[string]any{
		"instanceId": "ivis00000001",
		"items": []map[string]string{
			{"callNumber": "813.6 MUI", "location": "Main - Adult Fiction", "barcode": "300123", "note": "signed copy"},
			{"callNumber": "813.6 MUI c.2", "location": "Branch"},
		},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("put items = %d %s", rec.Code, rec.Body.String())
	}

	// Editor read-back.
	rec = request(t, h, http.MethodGet, "/v1/works/"+workID+"/items", "lib-token", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("get items = %d %s", rec.Code, rec.Body.String())
	}
	var got struct {
		Items map[string][]bibframe.Item `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	items := got.Items["ivis00000001"]
	if len(items) != 2 || items[0].CallNumber != "813.6 MUI" || items[0].Barcode != "300123" || items[1].Location != "Branch" {
		t.Fatalf("items = %+v", items)
	}

	// Projection carries them.
	grain, _, _ = bs.Get(t.Context(), bibframe.GrainPath(workID))
	cat, err := project.Project(grain, "overdrive")
	if err != nil || len(cat.Works) != 1 || len(cat.Works[0].Instances) != 1 {
		t.Fatalf("projection = %+v, %v", cat, err)
	}
	proj := cat.Works[0].Instances[0].Items
	if len(proj) != 2 || proj[0].CallNumber != "813.6 MUI" || proj[0].Note != "signed copy" || proj[1].Location != "Branch" {
		t.Fatalf("projected items = %+v", proj)
	}

	// Replace shrinks the set (no stale item statements). The first save moved
	// the etag, so rebase on the fresh one.
	_, etag = getItems(t, h)
	rec = request(t, h, http.MethodPut, "/v1/works/"+workID+"/items", "lib-token", etag, map[string]any{
		"instanceId": "ivis00000001",
		"items":      []map[string]string{{"callNumber": "813.6 MUI", "location": "Main"}},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("replace items = %d", rec.Code)
	}
	grain, _, _ = bs.Get(t.Context(), bibframe.GrainPath(workID))
	cat, _ = project.Project(grain, "overdrive")
	if got := cat.Works[0].Instances[0].Items; len(got) != 1 || got[0].Location != "Main" {
		t.Fatalf("after replace = %+v", got)
	}
}

func TestDuplicatesWorklist(t *testing.T) {
	bs := blob.NewMem()
	verifier := staffVerifier{"lib-token": {Email: "lib@example.org", Roles: []auth.Role{auth.RoleLibrarian}}}
	h := New(Deps{Blob: bs, DB: store.NewMem(), Verifier: verifier})
	// Two works with the same author+title+language clustering key, one
	// bystander.
	for _, id := range []string{"wdupa0000001", "wdupb0000001"} {
		seedTypedWork(t, bs, id, nil, "")
	}
	seedBatchWork(t, bs, "wother000001", "Something Else")

	rec := request(t, h, http.MethodGet, "/v1/duplicates", "lib-token", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("duplicates = %d %s", rec.Code, rec.Body.String())
	}
	var got struct {
		Groups []struct {
			Key   string `json:"key"`
			Works []struct {
				WorkID string `json:"workId"`
				Title  string `json:"title"`
			} `json:"works"`
		} `json:"groups"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Groups) != 1 || len(got.Groups[0].Works) != 2 {
		t.Fatalf("groups = %+v", got.Groups)
	}
	ids := got.Groups[0].Works[0].WorkID + got.Groups[0].Works[1].WorkID
	if !strings.Contains(ids, "wdupa0000001") || !strings.Contains(ids, "wdupb0000001") {
		t.Fatalf("group works = %+v", got.Groups[0].Works)
	}
	if got.Groups[0].Works[0].Title != "A Book" {
		t.Fatalf("titles missing: %+v", got.Groups[0].Works)
	}
}

// TestItemWritesRejectPhantomInstance covers both item write
// routes refuse an instanceId the work's grain does not describe -- a typo
// or an id copied from another record used to graft holdings onto a
// phantom IRI no reader enumerates, consuming real barcodes.
func TestItemWritesRejectPhantomInstance(t *testing.T) {
	h, bs := newMaintenanceAPI(t)
	const workID = "wvis00000001"
	grain, _, _ := bs.Get(t.Context(), bibframe.GrainPath(workID))
	withInst := string(grain) +
		`<#wvis00000001Work> <http://id.loc.gov/ontologies/bibframe/hasInstance> <#ivis00000001Instance> <feed:overdrive> .` + "\n" +
		`<#ivis00000001Instance> <http://www.w3.org/1999/02/22-rdf-syntax-ns#type> <http://id.loc.gov/ontologies/bibframe/Instance> <feed:overdrive> .` + "\n"
	if _, err := bs.Put(t.Context(), bibframe.GrainPath(workID), []byte(withInst), blob.PutOptions{}); err != nil {
		t.Fatal(err)
	}
	before, _, _ := bs.Get(t.Context(), bibframe.GrainPath(workID))

	// PUT items with a phantom instance -> 400, grain untouched. The token is
	// valid, so the 400 comes from SetItems rather than the precondition.
	_, etag := getItems(t, h)
	rec := request(t, h, http.MethodPut, "/v1/works/"+workID+"/items", "lib-token", etag, map[string]any{
		"instanceId": "izzzzzzphantom",
		"items":      []map[string]string{{"barcode": "999001"}},
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("put phantom = %d %s", rec.Code, rec.Body.String())
	}

	// Bulk add with a phantom instance -> 400, dryRun included.
	for _, dry := range []bool{true, false} {
		rec = request(t, h, http.MethodPost, "/v1/works/"+workID+"/items/bulk", "lib-token", "", map[string]any{
			"instanceId": "izzzzzzphantom", "count": 2, "barcodePrefix": "ZZ", "dryRun": dry,
		})
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("bulk phantom (dry=%v) = %d %s", dry, rec.Code, rec.Body.String())
		}
	}

	after, _, _ := bs.Get(t.Context(), bibframe.GrainPath(workID))
	if string(before) != string(after) {
		t.Fatalf("grain mutated by rejected writes:\n%s", after)
	}

	// The real instance still accepts writes. The refused phantom wrote nothing,
	// so the token read above is still current.
	rec = request(t, h, http.MethodPut, "/v1/works/"+workID+"/items", "lib-token", etag, map[string]any{
		"instanceId": "ivis00000001",
		"items":      []map[string]string{{"barcode": "300456"}},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("put real instance = %d %s", rec.Code, rec.Body.String())
	}
}
