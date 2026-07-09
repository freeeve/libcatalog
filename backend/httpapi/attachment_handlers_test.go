package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/freeeve/libcat/bibframe"
)

// TestAttachmentLifecycle covers tasks/229: upload records the editorial
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
	for _, bad := range []string{"..", ".env", "a%2Fb.pdf"} {
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
