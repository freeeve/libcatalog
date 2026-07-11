package httpapi

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/freeeve/libcat/storage/blob"
)

// coverZip builds a zip whose entries are name -> bytes, in order.
func coverZip(t *testing.T, entries ...[2]any) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range entries {
		w, err := zw.Create(e[0].(string))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(e[1].([]byte)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// rollbackFailBlob refuses every cover byte write, and refuses the grain write
// that would undo the statement -- the second grain Put after the test arms it.
// The first (the cover statement itself) lands, so the record is left claiming
// a cover that cannot be rolled back.
//
// Arming is explicit because seeding a work is itself a grain Put, and a
// counter that started at construction would fail the statement write instead
// of its rollback -- which is a plain skip, not the case under test.
type rollbackFailBlob struct {
	blob.Store
	coverPrefix string
	armed       bool
	grainPuts   int
}

func (f *rollbackFailBlob) arm() { f.armed = true }

func (f *rollbackFailBlob) Put(ctx context.Context, path string, data []byte, opts blob.PutOptions) (string, error) {
	if f.armed && strings.HasPrefix(path, f.coverPrefix) {
		return "", errStorage
	}
	if f.armed && strings.HasPrefix(path, "data/works/") {
		f.grainPuts++
		if f.grainPuts > 1 {
			return "", errStorage
		}
	}
	return f.Store.Put(ctx, path, data, opts)
}

type batchResponse struct {
	Applied int                `json:"applied"`
	Skipped int                `json:"skipped"`
	Failed  int                `json:"failed"`
	Results []coverBatchResult `json:"results"`
}

func postCoverBatch(t *testing.T, h http.Handler, zipped []byte) (*httptest.ResponseRecorder, batchResponse) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/covers/batch", bytes.NewReader(zipped))
	req.Header.Set("Authorization", "Bearer lib-token")
	req.Header.Set("Content-Type", "application/zip")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var out batchResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode %q: %v", rec.Body, err)
	}
	return rec, out
}

func resultFor(t *testing.T, resp batchResponse, file string) coverBatchResult {
	t.Helper()
	for _, r := range resp.Results {
		if r.File == file {
			return r
		}
	}
	t.Fatalf("no result for %q in %+v", file, resp.Results)
	return coverBatchResult{}
}

// Control: the batch applies a cover when the store works, and counts it.
func TestCoverBatchHappyPath(t *testing.T) {
	bs := blob.NewMem()
	h, queue := newCoverAPI(t, bs)
	seedWorkGrain(t, bs)

	rec, resp := postCoverBatch(t, h, coverZip(t, [2]any{editWorkID + ".png", pngBytes}))
	if rec.Code != http.StatusOK {
		t.Fatalf("batch = %d %s", rec.Code, rec.Body)
	}
	if resp.Applied != 1 || resp.Skipped != 0 || resp.Failed != 0 {
		t.Fatalf("counts = applied %d, skipped %d, failed %d", resp.Applied, resp.Skipped, resp.Failed)
	}
	if got := recordedCover(t, bs); got != "covers/"+editWorkID+".png" {
		t.Fatalf("recorded cover = %q", got)
	}
	if code := publicCover(t, h, "png"); code != http.StatusOK {
		t.Fatalf("public GET = %d", code)
	}
	if got := coverAudit(t, queue); len(got) != 1 || got[0] != "COVER_SET" {
		t.Fatalf("audit = %v", got)
	}
}

// Control: a genuinely skipped entry names no work and changes nothing. This
// is what "skipped" has to mean for the word to be worth printing (the
// reporter's F4).
func TestCoverBatchSkippedEntryTouchesNothing(t *testing.T) {
	bs := blob.NewMem()
	h, queue := newCoverAPI(t, bs)
	seedWorkGrain(t, bs)

	rec, resp := postCoverBatch(t, h, coverZip(t, [2]any{"zz-not-a-work.png", pngBytes}))
	if rec.Code != http.StatusOK {
		t.Fatalf("batch = %d %s", rec.Code, rec.Body)
	}
	if resp.Applied != 0 || resp.Skipped != 1 || resp.Failed != 0 {
		t.Fatalf("counts = applied %d, skipped %d, failed %d", resp.Applied, resp.Skipped, resp.Failed)
	}
	got := resultFor(t, resp, "zz-not-a-work.png")
	if got.WorkID != "" || got.Skipped == "" || got.Failed != "" {
		t.Fatalf("skipped entry = %+v", got)
	}
	if recordedCover(t, bs) != "" {
		t.Fatal("a skipped entry changed a record")
	}
	if len(coverAudit(t, queue)) != 0 {
		t.Fatal("a skipped entry was audited")
	}
}

// a failed byte Put left the grain claiming a cover, reported the
// entry as "skipped", excluded it from applied, and wrote no audit entry --
// inside a 200. The compensation makes it a true skip again.
func TestCoverBatchFailedPutLeavesNoPhantom(t *testing.T) {
	bs := &flakyBlob{Store: blob.NewMem(), failPutPrefix: "data/covers/"}
	h, queue := newCoverAPI(t, bs)
	seedWorkGrain(t, bs)

	rec, resp := postCoverBatch(t, h, coverZip(t, [2]any{editWorkID + ".png", pngBytes}))
	if rec.Code != http.StatusMultiStatus {
		t.Fatalf("a batch whose only entry failed = %d, want 207", rec.Code)
	}
	if got := recordedCover(t, bs); got != "" {
		t.Fatalf("the record claims cover %q whose bytes were never stored", got)
	}
	if code := publicCover(t, h, "png"); code != http.StatusNotFound {
		t.Fatalf("public GET = %d, want 404", code)
	}
	if resp.Applied != 0 || resp.Failed != 1 || resp.Skipped != 0 {
		t.Fatalf("counts = applied %d, skipped %d, failed %d; a store failure is not a skip",
			resp.Applied, resp.Skipped, resp.Failed)
	}
	got := resultFor(t, resp, editWorkID+".png")
	if got.Failed == "" {
		t.Fatalf("entry = %+v, want a failed reason", got)
	}
	if got.Skipped != "" {
		t.Fatalf("entry = %+v, want the store failure reported as failed, not skipped", got)
	}
	if got.Changed {
		t.Fatalf("entry = %+v: the rollback succeeded, so the record did not change", got)
	}
	// Nothing changed, so nothing is audited.
	if len(coverAudit(t, queue)) != 0 {
		t.Fatalf("audit = %v, want no entry", coverAudit(t, queue))
	}
}

// The reporter's exact zip: a failed entry, a bogus name, and a good one. The
// two must not be reported with the same word, and applied must count what was
// applied.
func TestCoverBatchDistinguishesFailedFromSkipped(t *testing.T) {
	bs := &flakyBlob{Store: blob.NewMem(), failPutPrefix: "data/covers/" + editWorkID[:2]}
	h, queue := newCoverAPI(t, bs)
	seedWorkGrain(t, bs)
	seedOtherWork(t, bs)

	rec, resp := postCoverBatch(t, h, coverZip(t,
		[2]any{editWorkID + ".png", pngBytes},
		[2]any{"zz-not-a-work.png", pngBytes},
		[2]any{otherWorkID + ".png", pngBytes},
	))
	if rec.Code != http.StatusMultiStatus {
		t.Fatalf("a batch with one failure = %d, want 207", rec.Code)
	}
	if resp.Applied != 1 || resp.Skipped != 1 || resp.Failed != 1 {
		t.Fatalf("counts = applied %d, skipped %d, failed %d, want 1/1/1", resp.Applied, resp.Skipped, resp.Failed)
	}
	if bad := resultFor(t, resp, editWorkID+".png"); bad.Failed == "" || bad.Skipped != "" {
		t.Fatalf("the store failure = %+v", bad)
	}
	if skip := resultFor(t, resp, "zz-not-a-work.png"); skip.Skipped == "" || skip.Failed != "" {
		t.Fatalf("the bogus name = %+v", skip)
	}
	if ok := resultFor(t, resp, otherWorkID+".png"); ok.Cover == "" || ok.Failed != "" || ok.Skipped != "" {
		t.Fatalf("the good entry = %+v", ok)
	}
	// Only the applied entry is audited, and it names the work it changed.
	if got := coverAudit(t, queue); len(got) != 1 || got[0] != "COVER_SET" {
		t.Fatalf("audit = %v", got)
	}
}

// A batch entry that fails while replacing an existing cover must restore the
// cover it replaced, not clear it: the previous cover's bytes are still stored
// and still serving publicly, so clearing the statement orphans a working
// public image to report a failed one.
func TestCoverBatchFailedReplacementRestoresThePreviousCover(t *testing.T) {
	bs := &flakyBlob{Store: blob.NewMem()}
	h, _ := newCoverAPI(t, bs)
	seedWorkGrain(t, bs)

	if rec := setCover(t, h, "image/jpeg", jpegBytes); rec.Code != http.StatusOK {
		t.Fatalf("stage: %d %s", rec.Code, rec.Body)
	}
	bs.failPutPrefix = "data/covers/"

	rec, resp := postCoverBatch(t, h, coverZip(t, [2]any{editWorkID + ".png", pngBytes}))
	if rec.Code != http.StatusMultiStatus || resp.Failed != 1 {
		t.Fatalf("batch = %d, failed %d", rec.Code, resp.Failed)
	}
	if got, want := recordedCover(t, bs), "covers/"+editWorkID+".jpg"; got != want {
		t.Fatalf("recorded cover = %q, want the surviving %q", got, want)
	}
	if code := publicCover(t, h, "jpg"); code != http.StatusOK {
		t.Fatalf("the previous cover stopped serving: %d", code)
	}
}

// A rollback that itself fails leaves the record changed. That entry must say
// so -- it is the one entry an operator has to go fix by hand -- and it must be
// audited, because the record really did change.
func TestCoverBatchUndoableFailureIsMarkedChangedAndAudited(t *testing.T) {
	bs := &rollbackFailBlob{Store: blob.NewMem(), coverPrefix: "data/covers/"}
	h, queue := newCoverAPI(t, bs)
	seedWorkGrain(t, bs)
	bs.arm()

	rec, resp := postCoverBatch(t, h, coverZip(t, [2]any{editWorkID + ".png", pngBytes}))
	if rec.Code != http.StatusMultiStatus {
		t.Fatalf("batch = %d, want 207", rec.Code)
	}
	got := resultFor(t, resp, editWorkID+".png")
	if got.Failed == "" || !got.Changed {
		t.Fatalf("entry = %+v, want failed and changed", got)
	}
	if resp.Failed != 1 || resp.Applied != 0 {
		t.Fatalf("counts = applied %d, failed %d", resp.Applied, resp.Failed)
	}
	// The grain still claims the cover -- that is the state being reported.
	if got := recordedCover(t, bs); got == "" {
		t.Fatal("the premise failed: the rollback succeeded")
	}
	// The record changed, so the change is in the audit log naming the work.
	entries := auditRows(t, queue)
	if len(entries) != 1 || entries[0].WorkID != editWorkID {
		t.Fatalf("audit = %+v, want one entry naming the mutated work", entries)
	}
}

// A batch entry whose stale-cover sweep fails did apply its cover -- the record
// is right -- but the blob it replaced keeps serving from its own public URL.
// That is a failure, not a clean success, and it is not a phantom either.
func TestCoverBatchFailedSweepIsReportedButNotAPhantom(t *testing.T) {
	bs := &flakyBlob{Store: blob.NewMem()}
	h, queue := newCoverAPI(t, bs)
	seedWorkGrain(t, bs)

	if rec := setCover(t, h, "image/jpeg", jpegBytes); rec.Code != http.StatusOK {
		t.Fatalf("stage: %d %s", rec.Code, rec.Body)
	}
	bs.failDeletePrefix = "data/covers/"

	rec, resp := postCoverBatch(t, h, coverZip(t, [2]any{editWorkID + ".png", pngBytes}))
	if rec.Code != http.StatusMultiStatus {
		t.Fatalf("batch = %d, want 207", rec.Code)
	}
	if resp.Failed != 1 || resp.Applied != 0 {
		t.Fatalf("counts = applied %d, failed %d", resp.Applied, resp.Failed)
	}
	got := resultFor(t, resp, editWorkID+".png")
	if got.Failed == "" || got.Cover != "covers/"+editWorkID+".png" {
		t.Fatalf("entry = %+v, want the new cover recorded and the sweep reported", got)
	}
	if got.Changed {
		t.Fatalf("entry = %+v: the record is correct, so nothing needs repairing by hand", got)
	}
	// The new cover applied, so it is audited as an ordinary COVER_SET.
	entries := auditRows(t, queue)
	if len(entries) != 2 || entries[0].Note != "covers/"+editWorkID+".png (batch)" {
		t.Fatalf("audit = %+v", entries)
	}
	// The premise: the replaced jpeg is still public.
	if code := publicCover(t, h, "jpg"); code != http.StatusOK {
		t.Fatalf("the stale jpeg = %d; the premise is that it survives", code)
	}
}

// A batch that applies every entry stays a plain 200.
func TestCoverBatchAllAppliedIsOK(t *testing.T) {
	bs := blob.NewMem()
	h, _ := newCoverAPI(t, bs)
	seedWorkGrain(t, bs)
	seedOtherWork(t, bs)

	rec, resp := postCoverBatch(t, h, coverZip(t,
		[2]any{editWorkID + ".png", pngBytes},
		[2]any{otherWorkID + ".png", pngBytes},
	))
	if rec.Code != http.StatusOK || resp.Applied != 2 {
		t.Fatalf("batch = %d, applied %d", rec.Code, resp.Applied)
	}
}
