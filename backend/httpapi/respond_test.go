// writeJSON must never emit a success status for a body it could not
// encode: a NaN in a float field previously produced a silent 200 with an
// empty body every client read as "no data" (task 416).
package httpapi

import (
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWriteJSONEncodeFailureIs500(t *testing.T) {
	rec := httptest.NewRecorder()
	writeJSON(rec, http.StatusOK, map[string]float64{"benchmark": math.NaN()})
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "response encoding failed") {
		t.Fatalf("body = %q, want the encoding-failure error", rec.Body)
	}

	rec = httptest.NewRecorder()
	writeJSON(rec, http.StatusOK, map[string]string{"ok": "yes"})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"ok":"yes"`) {
		t.Fatalf("happy path = %d %q", rec.Code, rec.Body)
	}
}
