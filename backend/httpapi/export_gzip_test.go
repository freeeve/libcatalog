// exports are gzipped at rest. The download route decides what the
// client is told about that -- a .gz artifact for the machine formats, a
// transparent Content-Encoding for CSV, and plain bytes for a client that
// refuses gzip (curl sends no Accept-Encoding at all).
package httpapi

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/freeeve/libcat/storage/blob"

	"github.com/freeeve/libcat/backend/auth"
	"github.com/freeeve/libcat/backend/batch"
	"github.com/freeeve/libcat/backend/export"
	"github.com/freeeve/libcat/backend/store"
)

// newExportAPI seeds two works and wires the export service over them.
func newExportAPI(t *testing.T) http.Handler {
	t.Helper()
	bs := blob.NewMem()
	seedBatchWork(t, bs, "wgz000000001", "Gideon the Ninth")
	seedBatchWork(t, bs, "wgz000000002", "Harrow the Ninth")
	db := store.NewMem()
	exports, err := export.New(store.NewMem(), bs, "overdrive", []byte("0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	verifier := staffVerifier{"lib-token": {Email: "lib@example.org", Roles: []auth.Role{auth.RoleLibrarian}}}
	return New(Deps{
		Blob: bs, DB: db, Verifier: verifier,
		Batch:   &batch.Service{Blob: bs, DB: db, Mapper: testMapper()},
		Exports: exports,
	})
}

// createExport queues a job over both works (small enough to run in-request)
// and returns its download URL.
func createExport(t *testing.T, h http.Handler, format string) string {
	t.Helper()
	rec := request(t, h, http.MethodPost, "/v1/exports", "lib-token", "", map[string]any{
		"format":    format,
		"selection": map[string]any{"workIds": []string{"wgz000000001", "wgz000000002"}},
	})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("create %s export = %d %s", format, rec.Code, rec.Body.String())
	}
	var job struct {
		Status      string `json:"status"`
		DownloadURL string `json:"downloadUrl"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &job); err != nil {
		t.Fatal(err)
	}
	if job.Status != "DONE" || job.DownloadURL == "" {
		t.Fatalf("job = %+v", job)
	}
	return job.DownloadURL
}

// download issues the request with the given Accept-Encoding (empty = none, the
// curl default).
func download(t *testing.T, h http.Handler, url, acceptEncoding string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, url, nil)
	if acceptEncoding != "" {
		req.Header.Set("Accept-Encoding", acceptEncoding)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("download = %d %s", rec.Code, rec.Body.String())
	}
	return rec
}

func gunzipBody(t *testing.T, rec *httptest.ResponseRecorder) []byte {
	t.Helper()
	zr, err := gzip.NewReader(bytes.NewReader(rec.Body.Bytes()))
	if err != nil {
		t.Fatalf("body is not a gzip stream: %v", err)
	}
	out, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func isGzipBody(rec *httptest.ResponseRecorder) bool {
	b := rec.Body.Bytes()
	return len(b) >= 2 && b[0] == 0x1f && b[1] == 0x8b
}

// The machine formats hand over a real .gz file. No Content-Encoding: the bytes
// are the artifact, not a transport encoding of something else.
func TestDownloadNQuadsIsAGzArtifact(t *testing.T) {
	h := newExportAPI(t)
	rec := download(t, h, createExport(t, h, "nquads"), "gzip")

	if got := rec.Header().Get("Content-Type"); got != "application/gzip" {
		t.Fatalf("Content-Type = %q, want application/gzip", got)
	}
	if got := rec.Header().Get("Content-Encoding"); got != "" {
		t.Fatalf("Content-Encoding = %q; a .gz artifact must not also be transport-encoded, or the browser inflates it back to 2GB", got)
	}
	if cd := rec.Header().Get("Content-Disposition"); !strings.Contains(cd, ".nq.gz") {
		t.Fatalf("Content-Disposition = %q, want a .nq.gz filename", cd)
	}
	if !isGzipBody(rec) {
		t.Fatal("body is not gzip")
	}
	if !bytes.Contains(gunzipBody(t, rec), []byte("wgz000000001")) {
		t.Fatal("the artifact does not decompress to the export")
	}
}

// A .gz artifact is served identically whether or not the client accepts gzip:
// it is a file, not a negotiation.
func TestGzArtifactIgnoresAcceptEncoding(t *testing.T) {
	h := newExportAPI(t)
	url := createExport(t, h, "nquads")

	with := download(t, h, url, "gzip")
	without := download(t, h, url, "")

	if !bytes.Equal(with.Body.Bytes(), without.Body.Bytes()) {
		t.Fatal("a .gz artifact changed shape with Accept-Encoding")
	}
	if without.Header().Get("Content-Encoding") != "" {
		t.Fatal("a client refusing gzip was sent gzip in Content-Encoding")
	}
}

// CSV is the human format: the browser saves an ordinary .csv and Excel opens it.
func TestDownloadCSVIsTransparentlyGzipped(t *testing.T) {
	h := newExportAPI(t)
	rec := download(t, h, createExport(t, h, "csv"), "gzip")

	if got := rec.Header().Get("Content-Type"); got != "text/csv" {
		t.Fatalf("Content-Type = %q, want text/csv", got)
	}
	if got := rec.Header().Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("Content-Encoding = %q, want gzip", got)
	}
	if cd := rec.Header().Get("Content-Disposition"); !strings.Contains(cd, ".csv") || strings.Contains(cd, ".gz") {
		t.Fatalf("Content-Disposition = %q, want a plain .csv filename", cd)
	}
	if !bytes.Contains(gunzipBody(t, rec), []byte("wgz000000001")) {
		t.Fatal("the CSV does not decompress to the export")
	}
}

// curl sends no Accept-Encoding. It must get CSV, not gzip bytes labelled
// text/csv.
func TestDownloadCSVIsPlainForAClientThatDidNotAskForGzip(t *testing.T) {
	h := newExportAPI(t)
	rec := download(t, h, createExport(t, h, "csv"), "")

	if got := rec.Header().Get("Content-Encoding"); got != "" {
		t.Fatalf("Content-Encoding = %q for a client that never asked", got)
	}
	if isGzipBody(rec) {
		t.Fatal("a client that did not accept gzip was handed a gzip stream")
	}
	if !bytes.HasPrefix(rec.Body.Bytes(), []byte("id,title,")) {
		t.Fatalf("body = %.40q, want a CSV header", rec.Body.String())
	}
}

// "gzip;q=0" is a refusal, and a substring search for "gzip" reads it as consent.
func TestDownloadCSVHonoursAnExplicitGzipRefusal(t *testing.T) {
	h := newExportAPI(t)
	rec := download(t, h, createExport(t, h, "csv"), "gzip;q=0, identity")

	if rec.Header().Get("Content-Encoding") != "" || isGzipBody(rec) {
		t.Fatal("gzip;q=0 was read as consent to gzip")
	}
}

// The CSV response depends on a request header, so caches must be told.
func TestDownloadCSVVariesOnAcceptEncoding(t *testing.T) {
	h := newExportAPI(t)
	rec := download(t, h, createExport(t, h, "csv"), "gzip")

	if !strings.Contains(rec.Header().Get("Vary"), "Accept-Encoding") {
		t.Fatalf("Vary = %q, want Accept-Encoding: a shared cache would serve gzip to a client that refused it", rec.Header().Get("Vary"))
	}
}

// Both delivery shapes decompress to the same catalog, which is the property a
// librarian actually depends on.
func TestBothDeliveryShapesCarryTheSameCSV(t *testing.T) {
	h := newExportAPI(t)
	url := createExport(t, h, "csv")

	compressed := gunzipBody(t, download(t, h, url, "gzip"))
	plain := download(t, h, url, "").Body.Bytes()

	if !bytes.Equal(compressed, plain) {
		t.Fatal("the gzipped and plain CSV downloads differ")
	}
}

func TestAcceptsGzip(t *testing.T) {
	cases := []struct {
		header string
		want   bool
	}{
		{"", false},
		{"gzip", true},
		{"gzip, deflate, br", true},
		{"deflate", false},
		{"identity", false},
		{"*", true},
		{"gzip;q=0", false},
		{"gzip;q=0.0", false},
		{"gzip;q=1.0", true},
		{"deflate, gzip;q=0.5", true},
		{"*;q=0", false},
		{"  GZIP  ", true}, // tokens are case-insensitive
		{"gzipped", false}, // not a substring match
	}
	for _, tc := range cases {
		if got := acceptsGzip(tc.header); got != tc.want {
			t.Errorf("acceptsGzip(%q) = %v, want %v", tc.header, got, tc.want)
		}
	}
}
