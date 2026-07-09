package httpapi

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/freeeve/libcat/bibframe"
)

// TestCoverLifecycle covers tasks/215: upload records the editorial
// lcat:extra/cover statement and stores the bytes; the public GET serves
// them; DELETE removes both; phantom works and bad types are refused.
func TestCoverLifecycle(t *testing.T) {
	h, bs := newRecordsAPI(t)
	seedWorkGrain(t, bs)
	png := []byte("\x89PNG\r\n\x1a\nfakebytes")

	do := func(method, path, token, ct string, body []byte) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, bytes.NewReader(body))
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		if ct != "" {
			req.Header.Set("Content-Type", ct)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	// Wrong type and phantom work refuse.
	if rec := do(http.MethodPut, "/v1/works/"+editWorkID+"/cover", "lib-token", "text/plain", png); rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("text cover = %d", rec.Code)
	}
	if rec := do(http.MethodPut, "/v1/works/wzzzz00phantom/cover", "lib-token", "image/png", png); rec.Code != http.StatusNotFound {
		t.Fatalf("phantom work cover = %d %s", rec.Code, rec.Body)
	}

	// Upload, then the grain carries the editorial extra and GET serves it.
	rec := do(http.MethodPut, "/v1/works/"+editWorkID+"/cover", "lib-token", "image/png", png)
	if rec.Code != http.StatusOK {
		t.Fatalf("upload = %d %s", rec.Code, rec.Body)
	}
	grain, _, _ := bs.Get(t.Context(), bibframe.GrainPath(editWorkID))
	if !strings.Contains(string(grain), bibframe.ExtraPred+bibframe.CoverExtraKey) ||
		!strings.Contains(string(grain), "covers/"+editWorkID+".png") {
		t.Fatalf("editorial cover statement missing:\n%s", grain)
	}
	rec = do(http.MethodGet, "/covers/"+editWorkID+".png", "", "", nil)
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "image/png" || !bytes.Equal(rec.Body.Bytes(), png) {
		t.Fatalf("serve cover = %d %q", rec.Code, rec.Header().Get("Content-Type"))
	}

	// Remove: statement gone, bytes gone.
	if rec := do(http.MethodDelete, "/v1/works/"+editWorkID+"/cover", "lib-token", "", nil); rec.Code != http.StatusNoContent {
		t.Fatalf("delete = %d %s", rec.Code, rec.Body)
	}
	grain, _, _ = bs.Get(t.Context(), bibframe.GrainPath(editWorkID))
	if strings.Contains(string(grain), bibframe.ExtraPred+bibframe.CoverExtraKey) {
		t.Fatalf("cover statement survived delete:\n%s", grain)
	}
	if rec := do(http.MethodGet, "/covers/"+editWorkID+".png", "", "", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("cover served after delete = %d", rec.Code)
	}

	// Anonymous writes refuse.
	if rec := do(http.MethodPut, "/v1/works/"+editWorkID+"/cover", "", "image/png", png); rec.Code != http.StatusUnauthorized {
		t.Fatalf("anon upload = %d", rec.Code)
	}
}
