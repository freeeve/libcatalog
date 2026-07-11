package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/freeeve/libcodex/rdf"

	"github.com/freeeve/libcat/bibframe"
	"github.com/freeeve/libcat/storage/blob"

	"github.com/freeeve/libcat/backend/auth"
	"github.com/freeeve/libcat/backend/editor"
	"github.com/freeeve/libcat/backend/store"
)

const editWorkID = "wabc123def456"

func seedWorkGrain(t *testing.T, bs blob.Store) []byte {
	t.Helper()
	ds := &rdf.Dataset{}
	feed := bibframe.FeedGraph("overdrive")
	work := rdf.NewIRI(bibframe.WorkIRI(editWorkID))
	ds.Add(work, rdf.NewIRI("http://id.loc.gov/ontologies/bibframe/title"), rdf.NewLiteral("A Book", "", ""), feed)
	nq, err := ds.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bs.Put(t.Context(), bibframe.GrainPath(editWorkID), nq, blob.PutOptions{}); err != nil {
		t.Fatal(err)
	}
	return nq
}

func newRecordsAPI(t *testing.T) (http.Handler, blob.Store) {
	t.Helper()
	bs := blob.NewMem()
	verifier := staffVerifier{
		"lib-token": {Email: "lib@example.org", Roles: []auth.Role{auth.RoleLibrarian}},
		"mod-token": {Email: "mod@example.org", Roles: []auth.Role{auth.RoleModerator}},
	}
	return New(Deps{Blob: bs, DB: store.NewMem(), Verifier: verifier}), bs
}

func request(t *testing.T, h http.Handler, method, path, token, ifMatch string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	req := httptest.NewRequest(method, path, &buf)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if ifMatch != "" {
		req.Header.Set("If-Match", ifMatch)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func subjectPatch(uri string) editor.Patch {
	return editor.Patch{Add: []editor.Statement{{
		S: bibframe.WorkIRI(editWorkID),
		P: "http://id.loc.gov/ontologies/bibframe/subject",
		O: editor.Term{Kind: "iri", Value: uri},
	}}}
}

func TestRecordEditFlow(t *testing.T) {
	h, bs := newRecordsAPI(t)
	seedWorkGrain(t, bs)

	// Moderators cannot edit records.
	if rec := request(t, h, http.MethodGet, "/v1/works/"+editWorkID, "mod-token", "", nil); rec.Code != http.StatusForbidden {
		t.Fatalf("moderator read: %d", rec.Code)
	}

	rec := request(t, h, http.MethodGet, "/v1/works/"+editWorkID, "lib-token", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("read: %d %s", rec.Code, rec.Body)
	}
	var view grainView
	_ = json.Unmarshal(rec.Body.Bytes(), &view)
	if view.ETag == "" || !strings.Contains(view.NQuads, "A Book") || rec.Header().Get("ETag") != view.ETag {
		t.Fatalf("view = %+v", view)
	}

	// Missing If-Match refused outright.
	patch := subjectPatch("https://homosaurus.org/v4/homoit0001235")
	if rec := request(t, h, http.MethodPut, "/v1/works/"+editWorkID, "lib-token", "", patch); rec.Code != http.StatusPreconditionRequired {
		t.Fatalf("no if-match: %d", rec.Code)
	}

	// Valid edit.
	rec = request(t, h, http.MethodPut, "/v1/works/"+editWorkID, "lib-token", view.ETag, patch)
	if rec.Code != http.StatusOK {
		t.Fatalf("put: %d %s", rec.Code, rec.Body)
	}
	var putResp map[string]string
	_ = json.Unmarshal(rec.Body.Bytes(), &putResp)
	newTag := putResp["etag"]
	if newTag == "" || newTag == view.ETag {
		t.Fatalf("etag = %q", newTag)
	}

	// Stale token: 412 with the fresh state for a client-side rebase.
	rec = request(t, h, http.MethodPut, "/v1/works/"+editWorkID, "lib-token", view.ETag, subjectPatch("https://x"))
	if rec.Code != http.StatusPreconditionFailed {
		t.Fatalf("stale put: %d", rec.Code)
	}
	var fresh grainView
	_ = json.Unmarshal(rec.Body.Bytes(), &fresh)
	if fresh.ETag != newTag || !strings.Contains(fresh.NQuads, "homoit0001235") {
		t.Fatalf("fresh state = %+v", fresh)
	}

	// Whitelist enforced.
	rogue := editor.Patch{Add: []editor.Statement{{
		S: bibframe.WorkIRI(editWorkID), P: "http://evil.example/poison",
		O: editor.Term{Kind: "literal", Value: "x"},
	}}}
	if rec := request(t, h, http.MethodPut, "/v1/works/"+editWorkID, "lib-token", newTag, rogue); rec.Code != http.StatusBadRequest {
		t.Fatalf("rogue predicate: %d %s", rec.Code, rec.Body)
	}
}

func TestValidateDryRun(t *testing.T) {
	h, bs := newRecordsAPI(t)
	original := seedWorkGrain(t, bs)
	rec := request(t, h, http.MethodPost, "/v1/works/"+editWorkID+"/validate", "lib-token", "", subjectPatch("https://homosaurus.org/v4/x"))
	if rec.Code != http.StatusOK {
		t.Fatalf("validate: %d %s", rec.Code, rec.Body)
	}
	var resp struct {
		Diff editor.Diff `json:"diff"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if len(resp.Diff.Added) != 1 || len(resp.Diff.Removed) != 0 || !strings.Contains(resp.Diff.Added[0], "editorial:") {
		t.Fatalf("diff = %+v", resp.Diff)
	}
	// Nothing written.
	after, _, _ := bs.Get(t.Context(), bibframe.GrainPath(editWorkID))
	if !bytes.Equal(after, original) {
		t.Fatal("dry run wrote")
	}
}

// TestMutateMissingWork404 covers mutate routes distinguish a
// missing work (404, matching the read paths) from a real conflict (409).
func TestMutateMissingWork404(t *testing.T) {
	h, _ := newRecordsAPI(t)
	rec := request(t, h, http.MethodPost, "/v1/works/merge", "lib-token", "", map[string]string{
		"from": "wzzz999zzz999", "to": "wmissing00000",
	})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("merge into a missing work = %d %s, want 404", rec.Code, rec.Body)
	}
}

func TestMergeSplitBatch(t *testing.T) {
	h, bs := newRecordsAPI(t)
	_ = seedWorkGrain(t, bs)
	// The survivor grain carries an instance (the split below pins it) and
	// the retiring work really exists -- merge/split refuse phantom ids
	//.
	ds := &rdf.Dataset{}
	feed := bibframe.FeedGraph("overdrive")
	ds.Add(rdf.NewIRI(bibframe.WorkIRI(editWorkID)), rdf.NewIRI("http://id.loc.gov/ontologies/bibframe/title"), rdf.NewLiteral("A Book", "", ""), feed)
	ds.Add(rdf.NewIRI(bibframe.InstanceIRI("i1")), rdf.NewIRI("http://www.w3.org/1999/02/22-rdf-syntax-ns#type"), rdf.NewIRI("http://id.loc.gov/ontologies/bibframe/Instance"), feed)
	original, err := ds.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bs.Put(t.Context(), bibframe.GrainPath(editWorkID), original, blob.PutOptions{}); err != nil {
		t.Fatal(err)
	}
	const srcWorkID = "wsrc999src999"
	src := &rdf.Dataset{}
	src.Add(rdf.NewIRI(bibframe.WorkIRI(srcWorkID)), rdf.NewIRI("http://id.loc.gov/ontologies/bibframe/title"), rdf.NewLiteral("Retiring", "", ""), feed)
	srcNQ, err := src.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bs.Put(t.Context(), bibframe.GrainPath(srcWorkID), srcNQ, blob.PutOptions{}); err != nil {
		t.Fatal(err)
	}

	// Merge via API reproduces AddMergeMarker byte-for-byte.
	rec := request(t, h, http.MethodPost, "/v1/works/merge", "lib-token", "", map[string]string{
		"from": srcWorkID, "to": editWorkID,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("merge: %d %s", rec.Code, rec.Body)
	}
	want, err := bibframe.AddMergeMarker(original, srcWorkID, editWorkID)
	if err != nil {
		t.Fatal(err)
	}
	got, _, _ := bs.Get(t.Context(), bibframe.GrainPath(editWorkID))
	if !bytes.Equal(got, want) {
		t.Fatalf("merge grain differs from AddMergeMarker:\n%s\nvs\n%s", got, want)
	}

	// Split mints a new work id and records pins.
	rec = request(t, h, http.MethodPost, "/v1/works/split", "lib-token", "", map[string]any{
		"from": editWorkID, "instances": []string{"i1"},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("split: %d %s", rec.Code, rec.Body)
	}
	var split struct{ NewWork string }
	_ = json.Unmarshal(rec.Body.Bytes(), &split)
	if !strings.HasPrefix(split.NewWork, "w") {
		t.Fatalf("split = %s", rec.Body)
	}
	got, _, _ = bs.Get(t.Context(), bibframe.GrainPath(editWorkID))
	if !strings.Contains(string(got), "workAssignment") || !strings.Contains(string(got), split.NewWork) {
		t.Fatalf("split markers missing:\n%s", got)
	}

	// Batch dry-run then execute.
	batch := map[string]any{
		"workIds": []string{editWorkID, "wmissing000000"},
		"patch":   subjectPatch("https://homosaurus.org/v4/batch"),
		"dryRun":  true,
	}
	rec = request(t, h, http.MethodPost, "/v1/batch", "lib-token", "", batch)
	if rec.Code != http.StatusOK {
		t.Fatalf("batch dry: %d %s", rec.Code, rec.Body)
	}
	var out struct {
		Results []struct {
			WorkID string       `json:"workId"`
			ETag   string       `json:"etag"`
			Diff   *editor.Diff `json:"diff"`
			Error  string       `json:"error"`
		} `json:"results"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if len(out.Results) != 2 || out.Results[0].Diff == nil || out.Results[1].Error == "" {
		t.Fatalf("dry results = %s", rec.Body)
	}
	batch["dryRun"] = false
	rec = request(t, h, http.MethodPost, "/v1/batch", "lib-token", "", batch)
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out.Results[0].ETag == "" || out.Results[1].Error == "" {
		t.Fatalf("exec results = %s", rec.Body)
	}
	got, _, _ = bs.Get(t.Context(), bibframe.GrainPath(editWorkID))
	if !strings.Contains(string(got), "v4/batch") {
		t.Fatal("batch edit not applied")
	}
}

// TestSecondContradictoryMergeIsRefused is the gate: a loser already
// merged into one survivor cannot be merged into a different one -- that would
// leave two mergedInto markers and let the resolver pick a survivor by grain scan
// order. Re-merging into the same survivor stays idempotent.
func TestSecondContradictoryMergeIsRefused(t *testing.T) {
	h, bs := newRecordsAPI(t)
	feed := bibframe.FeedGraph("overdrive")
	// Three works: loser A, survivors B and C, each a real grain (merge refuses
	// phantom ids, and the survivor's grain is read by mutateWorkGrain).
	const loserA, survivorB, survivorC = "waaa111aaa111", "wbbb222bbb222", "wccc333ccc333"
	for _, id := range []string{loserA, survivorB, survivorC} {
		ds := &rdf.Dataset{}
		ds.Add(rdf.NewIRI(bibframe.WorkIRI(id)), rdf.NewIRI("http://id.loc.gov/ontologies/bibframe/title"), rdf.NewLiteral("W "+id, "", ""), feed)
		grain, err := ds.Canonical()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := bs.Put(t.Context(), bibframe.GrainPath(id), grain, blob.PutOptions{}); err != nil {
			t.Fatal(err)
		}
	}

	// First merge A->B is accepted.
	if rec := request(t, h, http.MethodPost, "/v1/works/merge", "lib-token", "",
		map[string]string{"from": loserA, "to": survivorB}); rec.Code != http.StatusOK {
		t.Fatalf("first merge A->B = %d %s", rec.Code, rec.Body)
	}

	// A second, contradictory merge A->C is refused (409), naming the survivor.
	rec := request(t, h, http.MethodPost, "/v1/works/merge", "lib-token", "",
		map[string]string{"from": loserA, "to": survivorC})
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), survivorB) {
		t.Fatalf("contradictory merge A->C = %d %s, want 409 naming %s", rec.Code, rec.Body, survivorB)
	}
	// C's grain must not have gained a marker for the refused merge.
	if c, _, _ := bs.Get(t.Context(), bibframe.GrainPath(survivorC)); strings.Contains(string(c), "mergedInto") {
		t.Fatalf("refused merge still wrote a marker into C:\n%s", c)
	}

	// Re-merging A into the SAME survivor B stays idempotent (200, not a conflict).
	if rec := request(t, h, http.MethodPost, "/v1/works/merge", "lib-token", "",
		map[string]string{"from": loserA, "to": survivorB}); rec.Code != http.StatusOK {
		t.Fatalf("idempotent re-merge A->B = %d %s, want 200", rec.Code, rec.Body)
	}
}

// TestSplitIsIdempotent is the gate: splitting the same instance twice --
// a retry, a double-click, a lost response -- must reuse the first split's Work rather
// than mint a second, so the source grain never carries two contradictory
// workAssignment pins for one instance.
func TestSplitIsIdempotent(t *testing.T) {
	h, bs := newRecordsAPI(t)
	feed := bibframe.FeedGraph("overdrive")
	ds := &rdf.Dataset{}
	ds.Add(rdf.NewIRI(bibframe.WorkIRI(editWorkID)), rdf.NewIRI("http://id.loc.gov/ontologies/bibframe/title"), rdf.NewLiteral("A Book", "", ""), feed)
	ds.Add(rdf.NewIRI(bibframe.InstanceIRI("i1")), rdf.NewIRI("http://www.w3.org/1999/02/22-rdf-syntax-ns#type"), rdf.NewIRI("http://id.loc.gov/ontologies/bibframe/Instance"), feed)
	grain, err := ds.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bs.Put(t.Context(), bibframe.GrainPath(editWorkID), grain, blob.PutOptions{}); err != nil {
		t.Fatal(err)
	}

	split := func() string {
		rec := request(t, h, http.MethodPost, "/v1/works/split", "lib-token", "", map[string]any{
			"from": editWorkID, "instances": []string{"i1"},
		})
		if rec.Code != http.StatusOK {
			t.Fatalf("split: %d %s", rec.Code, rec.Body)
		}
		var out struct{ NewWork string }
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return out.NewWork
	}

	first := split()
	second := split()
	if first != second {
		t.Errorf("second split minted %s, want the first split's %s reused", second, first)
	}

	got, _, _ := bs.Get(t.Context(), bibframe.GrainPath(editWorkID))
	pins, err := bibframe.ScanPins(got)
	if err != nil {
		t.Fatal(err)
	}
	if len(pins) != 1 {
		t.Fatalf("grain carries %d workAssignment pins, want 1: %v", len(pins), pins)
	}
	if pins[0].Instance != "i1" || pins[0].Work != first {
		t.Errorf("pin = %+v, want i1 -> %s", pins[0], first)
	}
}

func TestDraftsCRUD(t *testing.T) {
	h, _ := newRecordsAPI(t)
	rec := request(t, h, http.MethodPost, "/v1/drafts", "lib-token", "", map[string]any{
		"workId": editWorkID, "body": map[string]any{"ops": []any{}},
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	var d struct{ ID string }
	_ = json.Unmarshal(rec.Body.Bytes(), &d)

	rec = request(t, h, http.MethodPut, "/v1/drafts/"+d.ID, "lib-token", "", map[string]any{
		"workId": editWorkID, "body": map[string]any{"ops": []any{"set"}},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("update: %d", rec.Code)
	}
	rec = request(t, h, http.MethodGet, "/v1/drafts", "lib-token", "", nil)
	var listing struct{ Drafts []draft }
	_ = json.Unmarshal(rec.Body.Bytes(), &listing)
	if len(listing.Drafts) != 1 || listing.Drafts[0].ID != d.ID {
		t.Fatalf("list = %s", rec.Body)
	}
	rec = request(t, h, http.MethodGet, "/v1/drafts/"+d.ID, "lib-token", "", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"set"`) {
		t.Fatalf("get = %d %s", rec.Code, rec.Body)
	}
	if rec := request(t, h, http.MethodDelete, "/v1/drafts/"+d.ID, "lib-token", "", nil); rec.Code != http.StatusNoContent {
		t.Fatalf("delete: %d", rec.Code)
	}
	if rec := request(t, h, http.MethodGet, "/v1/drafts/"+d.ID, "lib-token", "", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("get after delete: %d", rec.Code)
	}
}

// TestDraftSlotIsUniquePerWork is the gate: a (user, work) pair owns
// exactly one draft slot, so repeated POSTs of the same work -- two tabs, two
// devices, an autosave retry -- never pile up rival drafts the resume banner
// then has to pick between by lowest random id. The last write wins the slot.
func TestDraftSlotIsUniquePerWork(t *testing.T) {
	h, _ := newRecordsAPI(t)
	post := func(op string) {
		rec := request(t, h, http.MethodPost, "/v1/drafts", "lib-token", "", map[string]any{
			"workId": editWorkID, "body": map[string]any{"ops": []any{op}},
		})
		if rec.Code != http.StatusCreated {
			t.Fatalf("post %q: %d %s", op, rec.Code, rec.Body)
		}
	}
	post("first")
	post("second")
	post("third")

	rec := request(t, h, http.MethodGet, "/v1/drafts", "lib-token", "", nil)
	var listing struct{ Drafts []draft }
	_ = json.Unmarshal(rec.Body.Bytes(), &listing)
	if len(listing.Drafts) != 1 {
		t.Fatalf("one (user, work) must hold one draft, got %d: %s", len(listing.Drafts), rec.Body)
	}
	if listing.Drafts[0].ID != editWorkID {
		t.Errorf("draft id = %q, want the work id %q", listing.Drafts[0].ID, editWorkID)
	}
	// The list projection carries no body -- the fan-out fix.
	if len(listing.Drafts[0].Body) != 0 {
		t.Errorf("list should omit body, got %s", listing.Drafts[0].Body)
	}
	// The surviving draft is the last write, read through the point GET.
	rec = request(t, h, http.MethodGet, "/v1/drafts/"+editWorkID, "lib-token", "", nil)
	if !strings.Contains(rec.Body.String(), `"third"`) {
		t.Errorf("point read = %s, want the last-written body", rec.Body)
	}
	// A different work gets its own slot.
	if rec := request(t, h, http.MethodPost, "/v1/drafts", "lib-token", "", map[string]any{
		"workId": "wother", "body": map[string]any{"ops": []any{}},
	}); rec.Code != http.StatusCreated {
		t.Fatalf("post other work: %d %s", rec.Code, rec.Body)
	}
	rec = request(t, h, http.MethodGet, "/v1/drafts", "lib-token", "", nil)
	_ = json.Unmarshal(rec.Body.Bytes(), &listing)
	if len(listing.Drafts) != 2 {
		t.Fatalf("two works, two slots, got %d: %s", len(listing.Drafts), rec.Body)
	}

	// A POST with no workId is refused rather than keyed to an empty slot.
	if rec := request(t, h, http.MethodPost, "/v1/drafts", "lib-token", "", map[string]any{
		"body": map[string]any{"ops": []any{}},
	}); rec.Code != http.StatusBadRequest {
		t.Errorf("post without workId = %d, want 400", rec.Code)
	}
}

// TestMergeSplitRejectPhantomIDs covers merge refuses a retiring
// work that has no grain, and split refuses instances the source grain does
// not describe -- both markers are permanent identity-resolver instructions
// with no removal route, so a typo must not write false provenance or
// strand an Instance on a workless id.
func TestMergeSplitRejectPhantomIDs(t *testing.T) {
	h, bs := newRecordsAPI(t)
	before := seedWorkGrain(t, bs)

	// Merge with an unknown retiring work -> 404, survivor untouched.
	rec := request(t, h, http.MethodPost, "/v1/works/merge", "lib-token", "", map[string]string{
		"from": "wzzzz00e2eghost", "to": editWorkID,
	})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("phantom merge = %d %s, want 404", rec.Code, rec.Body)
	}

	// Split pinning an instance the grain does not describe -> 400.
	rec = request(t, h, http.MethodPost, "/v1/works/split", "lib-token", "", map[string]any{
		"from": editWorkID, "instances": []string{"izzsplitphantom"},
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("phantom split = %d %s, want 400", rec.Code, rec.Body)
	}

	after, _, _ := bs.Get(t.Context(), bibframe.GrainPath(editWorkID))
	if !bytes.Equal(before, after) {
		t.Fatalf("grain mutated by rejected merge/split:\n%s", after)
	}
}
