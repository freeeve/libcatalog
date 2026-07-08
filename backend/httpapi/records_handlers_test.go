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

// TestMutateMissingWork404 covers tasks/115: mutate routes distinguish a
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
	original := seedWorkGrain(t, bs)

	// Merge via API reproduces AddMergeMarker byte-for-byte.
	rec := request(t, h, http.MethodPost, "/v1/works/merge", "lib-token", "", map[string]string{
		"from": "wzzz999zzz999", "to": editWorkID,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("merge: %d %s", rec.Code, rec.Body)
	}
	want, err := bibframe.AddMergeMarker(original, "wzzz999zzz999", editWorkID)
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
