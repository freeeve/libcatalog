// PUT /v1/works/{id}/items replaced an instance's holdings wholesale
// from a list the client built minutes ago, and read no If-Match. Two catalogers
// with the item panel open on the same record: the second save deleted the
// first's item, and both were told 200. A barcode names one physical copy, so the
// lost item is a shelf silently unlinked from the catalog.
package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/freeeve/libcat/bibframe"
	"github.com/freeeve/libcat/storage/blob"

	"github.com/freeeve/libcat/backend/auth"
	"github.com/freeeve/libcat/backend/store"
)

const lostUpdateWork = "wvis00000001"
const lostUpdateInst = "ivis00000001"

// seedInstance gives the maintenance fixture's work one instance to hold items.
func seedInstance(t *testing.T, bs blob.Store) {
	t.Helper()
	grain, _, err := bs.Get(t.Context(), bibframe.GrainPath(lostUpdateWork))
	if err != nil {
		t.Fatal(err)
	}
	withInst := string(grain) +
		`<#` + lostUpdateWork + `Work> <http://id.loc.gov/ontologies/bibframe/hasInstance> <#` + lostUpdateInst + `Instance> <feed:overdrive> .` + "\n" +
		`<#` + lostUpdateInst + `Instance> <http://www.w3.org/1999/02/22-rdf-syntax-ns#type> <http://id.loc.gov/ontologies/bibframe/Instance> <feed:overdrive> .` + "\n"
	if _, err := bs.Put(t.Context(), bibframe.GrainPath(lostUpdateWork), []byte(withInst), blob.PutOptions{}); err != nil {
		t.Fatal(err)
	}
}

// getItems reads the panel's view: the barcodes it would show, and the token it
// is handed to write back under.
func getItems(t *testing.T, h http.Handler) (barcodes []string, etag string) {
	t.Helper()
	rec := request(t, h, http.MethodGet, "/v1/works/"+lostUpdateWork+"/items", "lib-token", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("get items = %d %s", rec.Code, rec.Body.String())
	}
	var got struct {
		ETag  string                     `json:"etag"`
		Items map[string][]bibframe.Item `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	for _, it := range got.Items[lostUpdateInst] {
		barcodes = append(barcodes, it.Barcode)
	}
	return barcodes, got.ETag
}

// putItems writes the whole list back under ifMatch (empty = send no header).
func putItems(t *testing.T, h http.Handler, ifMatch string, barcodes ...string) *httptest.ResponseRecorder {
	t.Helper()
	items := make([]map[string]string, 0, len(barcodes))
	for _, b := range barcodes {
		items = append(items, map[string]string{"barcode": b, "location": "Main"})
	}
	return request(t, h, http.MethodPut, "/v1/works/"+lostUpdateWork+"/items", "lib-token", ifMatch,
		map[string]any{"instanceId": lostUpdateInst, "items": items})
}

func has(barcodes []string, want string) bool {
	for _, b := range barcodes {
		if b == want {
			return true
		}
	}
	return false
}

// lostUpdateAPI seeds the work, its instance, and one item, and returns the
// panel's starting view.
func lostUpdateAPI(t *testing.T) (http.Handler, string) {
	t.Helper()
	h, bs := newMaintenanceAPI(t)
	seedInstance(t, bs)
	if rec := putItems(t, h, "", "zzlu-seed-1"); rec.Code == http.StatusPreconditionRequired {
		// Once the route requires a token, seeding needs one too.
		_, etag := getItems(t, h)
		if rec := putItems(t, h, etag, "zzlu-seed-1"); rec.Code != http.StatusOK {
			t.Fatalf("seed = %d %s", rec.Code, rec.Body.String())
		}
	}
	_, etag := getItems(t, h)
	return h, etag
}

// The finding. A and B both open the panel; A saves; B saves the list it read
// before A. A's copy must survive, and B must be told.
func TestItemsSaveDoesNotSilentlyDiscardAConcurrentEdit(t *testing.T) {
	h, shared := lostUpdateAPI(t)

	if rec := putItems(t, h, shared, "zzlu-seed-1", "zzlu-A"); rec.Code != http.StatusOK {
		t.Fatalf("A's save = %d %s", rec.Code, rec.Body.String())
	}
	after, moved := getItems(t, h)
	if !has(after, "zzlu-A") {
		t.Fatalf("A's save did not land: %v", after)
	}
	if moved == shared {
		t.Fatal("A's write did not move the etag, so B's token was not detectably stale")
	}

	rec := putItems(t, h, shared, "zzlu-seed-1", "zzlu-B")
	if rec.Code != http.StatusPreconditionFailed {
		t.Fatalf("B's stale save = %d, want 412; body %s", rec.Code, rec.Body.String())
	}

	final, _ := getItems(t, h)
	if !has(final, "zzlu-A") {
		t.Fatalf("A's copy was deleted by B's stale save: %v -- a shelf is now unlinked", final)
	}
	if has(final, "zzlu-B") {
		t.Fatalf("B's refused save was applied anyway: %v", final)
	}
}

// A missing token is 428, exactly as PUT /v1/works/{id} answers. The route is a
// client-token PUT; it must say so rather than guessing.
func TestItemsSaveWithoutIfMatchIsRefused(t *testing.T) {
	h, _ := lostUpdateAPI(t)

	rec := putItems(t, h, "", "zzlu-seed-1", "zzlu-X")
	if rec.Code != http.StatusPreconditionRequired {
		t.Fatalf("no If-Match = %d, want 428; body %s", rec.Code, rec.Body.String())
	}
	if errorOf(t, rec.Body.Bytes()) != "If-Match required" {
		t.Fatalf("message = %q", errorOf(t, rec.Body.Bytes()))
	}
	final, _ := getItems(t, h)
	if has(final, "zzlu-X") {
		t.Fatalf("a tokenless save was applied: %v", final)
	}
}

// I4: the header was not merely unsent, it was unread. An explicitly stale token
// must be honoured, not ignored.
func TestItemsSaveHonoursAnExplicitlyStaleToken(t *testing.T) {
	h, shared := lostUpdateAPI(t)
	if rec := putItems(t, h, shared, "zzlu-seed-1", "zzlu-A"); rec.Code != http.StatusOK {
		t.Fatalf("A's save = %d", rec.Code)
	}

	rec := putItems(t, h, shared, "zzlu-seed-1", "zzlu-C")
	if rec.Code != http.StatusPreconditionFailed {
		t.Fatalf("stale If-Match = %d, want 412: the header is not being read", rec.Code)
	}
}

// The 412 carries the fresh state and the fresh ETag, so the panel can show the
// other cataloger's edit and let this one rebase deliberately -- the contract
// PUT /v1/works/{id} already documents.
func TestItemsConflictCarriesTheFreshState(t *testing.T) {
	h, shared := lostUpdateAPI(t)
	if rec := putItems(t, h, shared, "zzlu-seed-1", "zzlu-A"); rec.Code != http.StatusOK {
		t.Fatalf("A's save = %d", rec.Code)
	}

	rec := putItems(t, h, shared, "zzlu-seed-1", "zzlu-B")
	if rec.Code != http.StatusPreconditionFailed {
		t.Fatalf("= %d, want 412", rec.Code)
	}
	if rec.Header().Get("ETag") == "" {
		t.Fatal("no ETag header on the 412: the client cannot rebase")
	}
	if rec.Header().Get("ETag") == shared {
		t.Fatal("the 412 returned the client's own stale token")
	}
	var view grainView
	if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	if view.NQuads == "" || view.ETag != rec.Header().Get("ETag") || view.WorkID != lostUpdateWork {
		t.Fatalf("412 body = %+v, want the fresh grain", view)
	}
	// The fresh state must actually contain the edit that beat this client.
	if !has(itemBarcodes(t, view.NQuads), "zzlu-A") {
		t.Fatal("the 412's fresh state does not show the concurrent edit")
	}
}

// itemBarcodes reads the barcodes out of a grain the 412 handed back.
func itemBarcodes(t *testing.T, nquads string) []string {
	t.Helper()
	items, err := bibframe.ItemsOf([]byte(nquads), lostUpdateInst)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, it.Barcode)
	}
	return out
}

// The token the GET hands out is the token the PUT accepts. If these ever drift,
// the panel can never save at all.
func TestItemsGetEtagIsAcceptedByPut(t *testing.T) {
	h, _ := lostUpdateAPI(t)
	_, etag := getItems(t, h)

	rec := putItems(t, h, etag, "zzlu-seed-1", "zzlu-A")
	if rec.Code != http.StatusOK {
		t.Fatalf("save with the GET's own etag = %d %s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("ETag") == etag {
		t.Fatal("a successful save must move the etag")
	}
}

// putConflictOnce lets the first Put through, then fails the next one the way a
// store does when another writer landed first. It stands in for the window
// between this handler's read and its own Put -- a window the handler's own etag
// comparison cannot see, because it compared before the window opened.
type putConflictOnce struct {
	blob.Store
	armed bool
}

func (b *putConflictOnce) Put(ctx context.Context, p string, data []byte, o blob.PutOptions) (string, error) {
	if b.armed && p == bibframe.GrainPath(lostUpdateWork) {
		b.armed = false
		return "", blob.ErrPreconditionFailed
	}
	return b.Store.Put(ctx, p, data, o)
}

// A cataloger whose token was current when the handler read the grain, and stale
// by the time it wrote, must still get a 412 rather than clobber. Only the
// store's own IfMatch can catch this one.
func TestItemsSaveDetectsAWriteThatLandsBetweenReadAndPut(t *testing.T) {
	_, bs := newMaintenanceAPI(t)
	seedInstance(t, bs)
	racy := &putConflictOnce{Store: bs}
	verifier := staffVerifier{"lib-token": {Email: "lib@example.org", Roles: []auth.Role{auth.RoleLibrarian}}}
	h := New(Deps{Blob: racy, DB: store.NewMem(), Verifier: verifier})

	_, etag := getItems(t, h)
	racy.armed = true

	rec := putItems(t, h, etag, "zzlu-seed-1", "zzlu-A")
	if rec.Code != http.StatusPreconditionFailed {
		t.Fatalf("= %d, want 412: a writer landed between the read and the Put", rec.Code)
	}
	if rec.Header().Get("ETag") == "" {
		t.Fatal("no fresh ETag on the 412")
	}
}

// Serial edits still work: rebasing on the fresh token succeeds. A conflict is a
// prompt to merge, not a wall.
func TestItemsSaveAfterRebaseSucceeds(t *testing.T) {
	h, shared := lostUpdateAPI(t)
	if rec := putItems(t, h, shared, "zzlu-seed-1", "zzlu-A"); rec.Code != http.StatusOK {
		t.Fatalf("A's save = %d", rec.Code)
	}
	if rec := putItems(t, h, shared, "zzlu-seed-1", "zzlu-B"); rec.Code != http.StatusPreconditionFailed {
		t.Fatalf("B's stale save = %d, want 412", rec.Code)
	}

	// B reloads, sees A's copy, keeps both, and saves again.
	current, fresh := getItems(t, h)
	if !has(current, "zzlu-A") {
		t.Fatalf("reload = %v", current)
	}
	if rec := putItems(t, h, fresh, "zzlu-seed-1", "zzlu-A", "zzlu-B"); rec.Code != http.StatusOK {
		t.Fatalf("B's rebased save = %d %s", rec.Code, rec.Body.String())
	}
	final, _ := getItems(t, h)
	if !has(final, "zzlu-A") || !has(final, "zzlu-B") || !has(final, "zzlu-seed-1") {
		t.Fatalf("final = %v, want all three", final)
	}
}
