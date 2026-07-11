package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/freeeve/libcat/backend/auth"
	"github.com/freeeve/libcat/backend/store"
	"github.com/freeeve/libcat/backend/suggest"
	"github.com/freeeve/libcat/bibframe"
	"github.com/freeeve/libcat/storage/blob"
)

func newCoverAPI(t *testing.T, bs blob.Store) (http.Handler, *suggest.Service) {
	t.Helper()
	db := store.NewMem()
	verifier := staffVerifier{"lib-token": {Email: "lib@example.org", Roles: []auth.Role{auth.RoleLibrarian}}}
	queue := suggest.New(db, nil, suggest.Caps{})
	return New(Deps{Blob: bs, DB: db, Verifier: verifier, Suggest: queue}), queue
}

// setCover is putCover bound to the work these tests seed.
func setCover(t *testing.T, h http.Handler, ct string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	return putCover(t, h, editWorkID, body, ct)
}

func deleteCover(t *testing.T, h http.Handler) *httptest.ResponseRecorder {
	t.Helper()
	return doJSON(t, h, http.MethodDelete, "/v1/works/"+editWorkID+"/cover", "lib-token", nil)
}

// coverAudit returns the audit actions in the order they were written; the
// store hands them back newest-first.
func coverAudit(t *testing.T, queue *suggest.Service) []string {
	t.Helper()
	got := auditActions(t, queue)
	slices.Reverse(got)
	return got
}

// publicCover fetches a cover through the unauthenticated route a reader uses.
func publicCover(t *testing.T, h http.Handler, ext string) int {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/covers/"+editWorkID+"."+ext, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code
}

// recordedCover reads the cover URL the grain claims -- what the OPAC's cover
// slot and the editor's Cover panel render.
func recordedCover(t *testing.T, bs blob.Store) string {
	t.Helper()
	grain, _, err := bs.Get(context.Background(), bibframe.GrainPath(editWorkID))
	if err != nil {
		t.Fatal(err)
	}
	url, err := bibframe.CoverOf(grain, editWorkID)
	if err != nil {
		t.Fatal(err)
	}
	return url
}

func coverBytesExist(t *testing.T, bs blob.Store, ext string) bool {
	t.Helper()
	_, _, err := bs.Get(context.Background(), bibframe.CoverBlobPath(editWorkID, ext))
	return err == nil
}

// Control: PUT, the public GET, and DELETE all work when the store works.
// Without this the induced-failure tests below prove nothing.
func TestCoverHappyPath(t *testing.T) {
	bs := blob.NewMem()
	h, queue := newCoverAPI(t, bs)
	seedWorkGrain(t, bs)

	if rec := setCover(t, h, "image/png", pngBytes); rec.Code != http.StatusOK {
		t.Fatalf("put: %d %s", rec.Code, rec.Body)
	}
	if got, want := recordedCover(t, bs), "covers/"+editWorkID+".png"; got != want {
		t.Fatalf("recorded cover = %q, want %q", got, want)
	}
	if code := publicCover(t, h, "png"); code != http.StatusOK {
		t.Fatalf("public GET after put = %d", code)
	}
	if rec := deleteCover(t, h); rec.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body)
	}
	if code := publicCover(t, h, "png"); code != http.StatusNotFound {
		t.Fatalf("public GET after delete = %d, want 404", code)
	}
	if recordedCover(t, bs) != "" {
		t.Fatal("delete left the cover statement behind")
	}
	if got := coverAudit(t, queue); !slices.Equal(got, []string{"COVER_SET", "COVER_REMOVE"}) {
		t.Fatalf("audit = %v", got)
	}
}

// Control: the induced failure is targeted. The grain store stays writable, so
// a 500 below is attributable to the cover byte write alone (the reporter's C3).
func TestCoverFailureIsTargetedAtTheCoverShard(t *testing.T) {
	bs := &flakyBlob{Store: blob.NewMem(), failPutPrefix: "data/covers/"}
	h, _ := newCoverAPI(t, bs)
	seedWorkGrain(t, bs)

	// The grain tree is untouched by failPutPrefix: a grain write still lands.
	ctx := context.Background()
	grain, _, err := bs.Get(ctx, bibframe.GrainPath(editWorkID))
	if err != nil {
		t.Fatal(err)
	}
	updated, err := bibframe.SetCover(grain, editWorkID, "covers/probe.png")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bs.Put(ctx, bibframe.GrainPath(editWorkID), updated, blob.PutOptions{}); err != nil {
		t.Fatalf("grain store is not writable, so the failure tests prove nothing: %v", err)
	}
	if got := recordedCover(t, bs); got != "covers/probe.png" {
		t.Fatalf("grain write did not land: %q", got)
	}
	// And the cover shard really is failing.
	if _, err := bs.Put(ctx, bibframe.CoverBlobPath(editWorkID, "png"), pngBytes, blob.PutOptions{}); err == nil {
		t.Fatal("the cover shard accepted a write; the induced failure is not armed")
	}
	_ = h
}

// a failed byte Put must not leave the record claiming a cover whose
// bytes were never stored. The phantom 404s at a public URL, the editor's Cover
// panel renders it, and re-uploading hits the same failing Put.
func TestFailedCoverUploadLeavesNoPhantom(t *testing.T) {
	bs := &flakyBlob{Store: blob.NewMem(), failPutPrefix: "data/covers/"}
	h, queue := newCoverAPI(t, bs)
	seedWorkGrain(t, bs)

	rec := setCover(t, h, "image/png", pngBytes)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("put with a failing cover store = %d %s, want 500", rec.Code, rec.Body)
	}
	if got := recordedCover(t, bs); got != "" {
		t.Fatalf("the record claims cover %q whose bytes were never stored", got)
	}
	if code := publicCover(t, h, "png"); code != http.StatusNotFound {
		t.Fatalf("public GET = %d, want 404", code)
	}
	// Nothing changed, so nothing is audited. The attempt is in the ERROR log.
	if got := auditActions(t, queue); len(got) != 0 {
		t.Fatalf("audit = %v, want no entry for a cover that was never set", got)
	}
}

// A failed replacement must restore the cover it was replacing, not clear it.
// The previous cover's bytes are still stored and still serving; clearing the
// statement would orphan a working, public image -- the lesson.
func TestFailedCoverReplacementRestoresThePreviousCover(t *testing.T) {
	mem := blob.NewMem()
	bs := &flakyBlob{Store: mem}
	h, queue := newCoverAPI(t, bs)
	seedWorkGrain(t, bs)

	if rec := setCover(t, h, "image/jpeg", jpegBytes); rec.Code != http.StatusOK {
		t.Fatalf("stage: %d %s", rec.Code, rec.Body)
	}
	bs.failPutPrefix = "data/covers/" // now every cover write fails

	if rec := setCover(t, h, "image/png", pngBytes); rec.Code != http.StatusInternalServerError {
		t.Fatalf("replace with a failing store = %d %s, want 500", rec.Code, rec.Body)
	}
	if got, want := recordedCover(t, bs), "covers/"+editWorkID+".jpg"; got != want {
		t.Fatalf("recorded cover = %q, want the surviving %q", got, want)
	}
	if code := publicCover(t, h, "jpg"); code != http.StatusOK {
		t.Fatalf("the previous cover stopped serving: %d", code)
	}
	if got := coverAudit(t, queue); !slices.Equal(got, []string{"COVER_SET"}) {
		t.Fatalf("audit = %v, want only the successful staging entry", got)
	}
}

// the teeth: DELETE answered 204 while the bytes kept serving at the
// public, unauthenticated URL, and the grain dropped the only reference that
// would ever have found them again. A takedown that looks done was not done.
func TestFailedCoverDeleteIsReportedAndKeepsTheRecordConsistent(t *testing.T) {
	mem := blob.NewMem()
	bs := &flakyBlob{Store: mem}
	h, queue := newCoverAPI(t, bs)
	seedWorkGrain(t, bs)

	if rec := setCover(t, h, "image/png", pngBytes); rec.Code != http.StatusOK {
		t.Fatalf("stage: %d %s", rec.Code, rec.Body)
	}
	bs.failDeletePrefix = "data/covers/"

	rec := deleteCover(t, h)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("delete with surviving bytes = %d %s, want 500", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), "still") {
		t.Fatalf("the error does not say the bytes survived: %s", rec.Body)
	}
	// The bytes are still public. The record must still point at them, or
	// nothing will ever collect them.
	if !coverBytesExist(t, mem, "png") {
		t.Fatal("the test's premise failed: the bytes were deleted")
	}
	if code := publicCover(t, h, "png"); code != http.StatusOK {
		t.Fatalf("public GET = %d; the premise is that the bytes survive", code)
	}
	if got, want := recordedCover(t, bs), "covers/"+editWorkID+".png"; got != want {
		t.Fatalf("recorded cover = %q, want the restored %q: the surviving bytes are orphaned", got, want)
	}
	if got := coverAudit(t, queue); !slices.Equal(got, []string{"COVER_SET"}) {
		t.Fatalf("audit = %v, want no COVER_REMOVE for a cover that was not removed", got)
	}

	// Once the store recovers, the takedown completes.
	bs.failDeletePrefix = ""
	if rec := deleteCover(t, h); rec.Code != http.StatusNoContent {
		t.Fatalf("delete after recovery = %d %s", rec.Code, rec.Body)
	}
	if code := publicCover(t, h, "png"); code != http.StatusNotFound {
		t.Fatalf("public GET after a successful delete = %d, want 404", code)
	}
	if recordedCover(t, bs) != "" {
		t.Fatal("a successful delete left the statement behind")
	}
}

// Replacing a JPEG with a PNG sweeps the JPEG. If that sweep fails the old
// image keeps serving at its own public URL while the record points at the new
// one -- the takedown failure, reached through a store error rather
// than through the ordering bug 243 fixed. It must not answer 200.
func TestFailedStaleCoverSweepIsReported(t *testing.T) {
	mem := blob.NewMem()
	bs := &flakyBlob{Store: mem}
	h, _ := newCoverAPI(t, bs)
	seedWorkGrain(t, bs)

	if rec := setCover(t, h, "image/jpeg", jpegBytes); rec.Code != http.StatusOK {
		t.Fatalf("stage: %d %s", rec.Code, rec.Body)
	}
	bs.failDeletePrefix = "data/covers/"

	rec := setCover(t, h, "image/png", pngBytes)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("replace whose sweep failed = %d %s, want 500", rec.Code, rec.Body)
	}
	// The new cover is stored and recorded -- the upload half worked, and a
	// retry must be able to re-run the sweep.
	if got, want := recordedCover(t, bs), "covers/"+editWorkID+".png"; got != want {
		t.Fatalf("recorded cover = %q, want %q", got, want)
	}
	if code := publicCover(t, h, "jpg"); code != http.StatusOK {
		t.Fatalf("the test's premise failed: the stale jpeg = %d", code)
	}

	bs.failDeletePrefix = ""
	if rec := setCover(t, h, "image/png", pngBytes); rec.Code != http.StatusOK {
		t.Fatalf("retry after recovery = %d %s", rec.Code, rec.Body)
	}
	if code := publicCover(t, h, "jpg"); code != http.StatusNotFound {
		t.Fatalf("the retry did not sweep the stale jpeg: %d", code)
	}
}

// A cover blob absent in the other two formats is the normal case: the sweep
// deletes three paths and finds one. ErrNotFound is the state a delete asks
// for, so it must not be reported as a failure.
func TestCoverSweepTreatsMissingBlobsAsSuccess(t *testing.T) {
	bs := blob.NewMem()
	h, _ := newCoverAPI(t, bs)
	seedWorkGrain(t, bs)

	if rec := setCover(t, h, "image/png", pngBytes); rec.Code != http.StatusOK {
		t.Fatalf("put: %d %s (webp and jpg never existed)", rec.Code, rec.Body)
	}
	if rec := deleteCover(t, h); rec.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body)
	}
}
