package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/freeeve/libcat/backend/auth"
	"github.com/freeeve/libcat/backend/editor"
	"github.com/freeeve/libcat/backend/store"
	"github.com/freeeve/libcat/backend/suggest"
	"github.com/freeeve/libcat/bibframe"
	"github.com/freeeve/libcat/storage/blob"
)

type opsSaveResponse struct {
	WorkID string      `json:"workId"`
	ETag   string      `json:"etag"`
	Diff   editor.Diff `json:"diff"`
}

// grainETag reads the current etag through the API, the way a client would.
func grainETag(t *testing.T, h http.Handler) string {
	t.Helper()
	rec := request(t, h, http.MethodGet, "/v1/works/"+editWorkID, "lib-token", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET work: %d", rec.Code)
	}
	var out struct {
		ETag string `json:"etag"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out.ETag
}

// postOps applies one op list at the given etag and returns the parsed response.
func postOps(t *testing.T, h http.Handler, ifMatch string, ops []map[string]any) (int, opsSaveResponse) {
	t.Helper()
	rec := request(t, h, http.MethodPost, "/v1/works/"+editWorkID+"/ops", "lib-token", ifMatch,
		map[string]any{"ops": ops})
	var out opsSaveResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func auditRows(t *testing.T, queue *suggest.Service) []suggest.AuditEntry {
	t.Helper()
	entries, err := queue.Audit(t.Context(), time.Now().UTC().Format("2006-01"))
	if err != nil {
		t.Fatal(err)
	}
	return entries
}

func setTitleOps(title string) []map[string]any {
	return []map[string]any{{
		"resource": "work", "path": "title", "action": "set",
		"values": []map[string]any{{"v": title}},
	}}
}

// the reported repro: applying the same ops three times wrote three
// RECORD_EDIT rows, all naming the same etag, two of them for saves whose diff
// was empty. The history must be a faithful account of what happened, and two
// of those three things did not happen.
func TestRepeatedIdenticalOpsSavesAuditOnce(t *testing.T) {
	h, bs, queue := newRecordsAPIWithQueue(t)
	seedWorkGrain(t, bs)

	ops := setTitleOps("A Better Book")

	code, first := postOps(t, h, grainETag(t, h), ops)
	if code != http.StatusOK {
		t.Fatalf("save #1: %d", code)
	}
	if first.Diff.Empty() {
		t.Fatal("save #1 changed nothing; the fixture cannot detect the bug")
	}
	if got := len(auditRows(t, queue)); got != 1 {
		t.Fatalf("after a real edit: %d audit rows, want 1", got)
	}

	// Saves #2 and #3 request the same state the record already has.
	for i, label := range []string{"save #2", "save #3"} {
		code, out := postOps(t, h, grainETag(t, h), ops)
		if code != http.StatusOK {
			t.Fatalf("%s: %d", label, code)
		}
		if !out.Diff.Empty() {
			t.Fatalf("%s: diff = %+v, want empty", label, out.Diff)
		}
		if out.ETag != first.ETag {
			t.Fatalf("%s: etag moved %s -> %s on a no-op save", label, first.ETag, out.ETag)
		}
		if got := len(auditRows(t, queue)); got != 1 {
			t.Fatalf("%s (no-op #%d): %d audit rows, want 1 -- a save that changed nothing was recorded", label, i+1, got)
		}
	}
}

// The audit note must say how much changed, not how much was asked for: "1 ops"
// read identically on a real edit and on a no-op. The counts are taken from the
// save's own reported diff, so the note and the response cannot drift apart.
func TestAuditNoteReportsQuadCounts(t *testing.T) {
	h, bs, queue := newRecordsAPIWithQueue(t)
	seedWorkGrain(t, bs)

	code, out := postOps(t, h, grainETag(t, h), setTitleOps("A Better Book"))
	if code != http.StatusOK {
		t.Fatalf("save: %d", code)
	}
	rows := auditRows(t, queue)
	if len(rows) != 1 {
		t.Fatalf("%d audit rows, want 1", len(rows))
	}
	want := fmt.Sprintf("1 ops, +%d/-%d quads", len(out.Diff.Added), len(out.Diff.Removed))
	if rows[0].Note != want {
		t.Fatalf("note = %q, want %q", rows[0].Note, want)
	}
	if len(out.Diff.Added) == 0 {
		t.Fatal("the fixture edit added no quads; the note would read the same on a no-op")
	}
}

// countingBlob counts writes. Observing the grain's bytes is not enough: the
// store is content-addressed, so a no-op Put leaves the grain and its etag
// exactly as they were. The defect was that the Put -- and the feed republish
// that follows it -- happened at all.
type countingBlob struct {
	blob.Store
	puts int
}

func (c *countingBlob) Put(ctx context.Context, path string, data []byte, opts blob.PutOptions) (string, error) {
	c.puts++
	return c.Store.Put(ctx, path, data, opts)
}

// A no-op save must not write the grain, because the write is what republishes
// it to the feed -- and a rebuild triggered by an edit that did not happen
// reprojects the whole site.
func TestNoOpOpsSaveDoesNotWriteTheGrain(t *testing.T) {
	cb := &countingBlob{Store: blob.NewMem()}
	db := store.NewMem()
	verifier := staffVerifier{"lib-token": {Email: "lib@example.org", Roles: []auth.Role{auth.RoleLibrarian}}}
	queue := suggest.New(db, nil, suggest.Caps{})
	h := New(Deps{Blob: cb, DB: db, Verifier: verifier, Suggest: queue})
	seedWorkGrain(t, cb)

	ops := setTitleOps("A Better Book")
	if code, _ := postOps(t, h, grainETag(t, h), ops); code != http.StatusOK {
		t.Fatal("first save failed")
	}
	writesAfterRealEdit := cb.puts
	if writesAfterRealEdit == 0 {
		t.Fatal("the real edit wrote nothing; the counter is not wired up")
	}

	code, out := postOps(t, h, grainETag(t, h), ops)
	if code != http.StatusOK {
		t.Fatalf("no-op save: %d", code)
	}
	if !out.Diff.Empty() {
		t.Fatalf("expected an empty diff, got %+v", out.Diff)
	}
	if cb.puts != writesAfterRealEdit {
		t.Fatalf("a no-op save wrote the grain: puts %d -> %d", writesAfterRealEdit, cb.puts)
	}
}

// PUT /v1/works/{id} never computed a diff at all, so the same defect lived
// there unreported: applying the same patch twice audited twice. (An empty
// Patch is refused outright, so the no-op has to be a patch that re-adds a
// statement the grain already carries -- which is exactly how 248's phantom
// Add reaches this handler.)
func TestRepeatedIdenticalPutAuditsOnce(t *testing.T) {
	h, bs, queue := newRecordsAPIWithQueue(t)
	seedWorkGrain(t, bs)

	patch := editor.Patch{Add: []editor.Statement{{
		S: bibframe.WorkIRI(editWorkID),
		P: "http://id.loc.gov/ontologies/bibframe/subject",
		O: editor.Term{Kind: "iri", Value: "https://homosaurus.org/v4/noop"},
	}}}

	rec := request(t, h, http.MethodPut, "/v1/works/"+editWorkID, "lib-token", grainETag(t, h), patch)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT #1: %d -- %s", rec.Code, rec.Body)
	}
	if got := len(auditRows(t, queue)); got != 1 {
		t.Fatalf("after a real PUT: %d audit rows, want 1", got)
	}

	// The same statement again: the grain already carries it.
	rec = request(t, h, http.MethodPut, "/v1/works/"+editWorkID, "lib-token", grainETag(t, h), patch)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT #2: %d -- %s", rec.Code, rec.Body)
	}
	if got := len(auditRows(t, queue)); got != 1 {
		t.Fatalf("%d audit rows after a PUT that changed nothing, want 1", got)
	}
}

// A real edit still writes, still moves the etag, and still audits.
func TestRealOpsSaveStillAudits(t *testing.T) {
	h, bs, queue := newRecordsAPIWithQueue(t)
	before := seedWorkGrain(t, bs)

	code, out := postOps(t, h, grainETag(t, h), setTitleOps("A Better Book"))
	if code != http.StatusOK {
		t.Fatalf("save: %d", code)
	}
	if out.Diff.Empty() {
		t.Fatal("a real edit reported an empty diff")
	}
	after, _, err := bs.Get(t.Context(), bibframe.GrainPath(editWorkID))
	if err != nil {
		t.Fatal(err)
	}
	if string(after) == string(before) {
		t.Fatal("the grain did not change on a real edit")
	}
	if got := len(auditRows(t, queue)); got != 1 {
		t.Fatalf("%d audit rows after a real edit, want 1", got)
	}
}
