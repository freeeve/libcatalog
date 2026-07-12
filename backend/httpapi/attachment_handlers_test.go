package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/freeeve/libcat/bibframe"
	"github.com/freeeve/libcat/storage/blob"
)

// TestAttachmentLifecycle covers upload records the editorial
// statement and stores the bytes; list and download are librarian-gated;
// delete removes both; phantom works, bad names, and anonymous callers
// refuse.
func TestAttachmentLifecycle(t *testing.T) {
	h, bs := newRecordsAPI(t)
	seedWorkGrain(t, bs)
	pdf := []byte("%PDF-1.4 fake attachment bytes")

	do := func(method, path, token string, body []byte) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, bytes.NewReader(body))
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	base := "/v1/works/" + editWorkID + "/attachments"

	// Refusals: phantom, traversal-shaped and dotfile names, empty body, anon.
	if rec := do(http.MethodPost, "/v1/works/wzzzz00phantom/attachments?name=a.pdf", "lib-token", pdf); rec.Code != http.StatusNotFound {
		t.Fatalf("phantom = %d %s", rec.Code, rec.Body)
	}
	// ".env" is a legal *name* -- it encodes to the segment
	// "a.env", so it can never be a dotfile on disk. Traversal still refuses.
	for _, bad := range []string{"..", "a%2Fb.pdf", "%2E%2E%2F%2E%2E%2Fgrain", strings.Repeat("x", 101)} {
		if rec := do(http.MethodPost, base+"?name="+bad, "lib-token", pdf); rec.Code != http.StatusBadRequest {
			t.Fatalf("name %q = %d", bad, rec.Code)
		}
	}
	if rec := do(http.MethodPost, base+"?name=a.pdf", "lib-token", nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("empty body = %d", rec.Code)
	}
	if rec := do(http.MethodPost, base+"?name=a.pdf", "", pdf); rec.Code != http.StatusUnauthorized {
		t.Fatalf("anon upload = %d", rec.Code)
	}

	// Upload -> statement in the grain, bytes stored, list shows it.
	if rec := do(http.MethodPost, base+"?name=invoice-2026.pdf", "lib-token", pdf); rec.Code != http.StatusCreated {
		t.Fatalf("upload = %d %s", rec.Code, rec.Body)
	}
	grain, _, _ := bs.Get(t.Context(), bibframe.GrainPath(editWorkID))
	if !strings.Contains(string(grain), "invoice-2026.pdf") {
		t.Fatalf("statement missing:\n%s", grain)
	}
	var list struct{ Attachments []string }
	rec := do(http.MethodGet, base, "lib-token", nil)
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || len(list.Attachments) != 1 || list.Attachments[0] != "invoice-2026.pdf" {
		t.Fatalf("list = %s (%v)", rec.Body, err)
	}

	// Download is gated and download-shaped.
	if rec := do(http.MethodGet, base+"/invoice-2026.pdf", "", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("anon download = %d", rec.Code)
	}
	rec = do(http.MethodGet, base+"/invoice-2026.pdf", "lib-token", nil)
	if rec.Code != http.StatusOK || !bytes.Equal(rec.Body.Bytes(), pdf) ||
		rec.Header().Get("Content-Type") != "application/octet-stream" ||
		!strings.Contains(rec.Header().Get("Content-Disposition"), "attachment") {
		t.Fatalf("download = %d ct=%q cd=%q", rec.Code, rec.Header().Get("Content-Type"), rec.Header().Get("Content-Disposition"))
	}

	// Delete removes statement and bytes.
	if rec := do(http.MethodDelete, base+"/invoice-2026.pdf", "lib-token", nil); rec.Code != http.StatusNoContent {
		t.Fatalf("delete = %d %s", rec.Code, rec.Body)
	}
	grain, _, _ = bs.Get(t.Context(), bibframe.GrainPath(editWorkID))
	if strings.Contains(string(grain), "invoice-2026.pdf") {
		t.Fatalf("statement survived delete:\n%s", grain)
	}
	if rec := do(http.MethodGet, base+"/invoice-2026.pdf", "lib-token", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("download after delete = %d", rec.Code)
	}
}

// TestAttachmentNamesDoNotCollide covers two documents named in a
// non-Latin script stay two documents, and an upload never lands on an
// existing attachment's bytes by accident.
func TestAttachmentNamesDoNotCollide(t *testing.T) {
	h, bs := newRecordsAPI(t)
	seedWorkGrain(t, bs)
	base := "/v1/works/" + editWorkID + "/attachments"

	do := func(method, path string, body []byte) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer lib-token")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	post := func(name string, body string) int {
		return do(http.MethodPost, base+"?name="+url.QueryEscape(name), []byte(body)).Code
	}
	get := func(name string) *httptest.ResponseRecorder {
		return do(http.MethodGet, base+"/"+url.PathEscape(name), nil)
	}

	// Two different documents, two different non-Latin names.
	if code := post("文書.pdf", "FIRST-DOCUMENT"); code != http.StatusCreated {
		t.Fatalf("文書.pdf = %d", code)
	}
	if code := post("資料.pdf", "SECOND-DOCUMENT"); code != http.StatusCreated {
		t.Fatalf("資料.pdf = %d", code)
	}
	var list struct{ Attachments []string }
	rec := do(http.MethodGet, base, nil)
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Attachments) != 2 {
		t.Fatalf("attachments = %v, want both names kept", list.Attachments)
	}
	// The first document's bytes are still its own.
	if rec := get("文書.pdf"); rec.Code != http.StatusOK || rec.Body.String() != "FIRST-DOCUMENT" {
		t.Fatalf("文書.pdf = %d %q -- overwritten", rec.Code, rec.Body)
	}
	if rec := get("資料.pdf"); rec.Code != http.StatusOK || rec.Body.String() != "SECOND-DOCUMENT" {
		t.Fatalf("資料.pdf = %d %q", rec.Code, rec.Body)
	}
	// The download names the file for any script, without breaking the header.
	if cd := get("文書.pdf").Header().Get("Content-Disposition"); !strings.Contains(cd, "attachment") || !strings.Contains(cd, "filename") {
		t.Fatalf("content-disposition = %q", cd)
	}

	// Re-POSTing a live name refuses rather than clobbering.
	if code := post("文書.pdf", "REPLACED"); code != http.StatusConflict {
		t.Fatalf("clobbering re-POST = %d, want 409", code)
	}
	if rec := get("文書.pdf"); rec.Body.String() != "FIRST-DOCUMENT" {
		t.Fatalf("refused POST still wrote: %q", rec.Body)
	}
	// ...unless replacement is asked for explicitly.
	if code := do(http.MethodPost, base+"?name="+url.QueryEscape("文書.pdf")+"&replace=true", []byte("REPLACED")).Code; code != http.StatusCreated {
		t.Fatalf("?replace=true = %d", code)
	}
	if rec := get("文書.pdf"); rec.Body.String() != "REPLACED" {
		t.Fatalf("replace=true did not write: %q", rec.Body)
	}
}

// TestAttachmentLegacyPathFallback covers the read path for attachments
// stored under the legacy layout, when the display name was the blob
// segment: the new encoding must not orphan their bytes.
func TestAttachmentLegacyPathFallback(t *testing.T) {
	h, bs := newRecordsAPI(t)
	grain := seedWorkGrain(t, bs)
	const name = "old-scan.pdf"

	// Simulate a pre-236 upload: statement plus bytes at the un-encoded path.
	updated, err := bibframe.SetAttachment(grain, editWorkID, name, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bs.Put(t.Context(), bibframe.GrainPath(editWorkID), updated, blob.PutOptions{}); err != nil {
		t.Fatal(err)
	}
	legacy := bibframe.LegacyAttachmentBlobPath(editWorkID, name)
	if _, err := bs.Put(t.Context(), legacy, []byte("LEGACY-BYTES"), blob.PutOptions{}); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/works/"+editWorkID+"/attachments/"+name, nil)
	req.Header.Set("Authorization", "Bearer lib-token")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Body.String() != "LEGACY-BYTES" {
		t.Fatalf("legacy download = %d %q", rec.Code, rec.Body)
	}
}
