package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// TestServeHandlerRange pins the reason `lcat serve` exists: the
// roaringrange reader fetches index artifacts with HTTP Range requests, so
// the preview server must answer 206 partial content with exactly the
// requested bytes -- python http.server ignores Range and silently breaks
// client-side browse.
func TestServeHandlerRange(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "browse-index.rrs"), []byte("0123456789abcdef"), 0o644); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(serveHandler(dir, false))
	defer srv.Close()

	req, err := http.NewRequest("GET", srv.URL+"/browse-index.rrs", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Range", "bytes=4-7")
	res, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusPartialContent || string(body) != "4567" {
		t.Fatalf("range response = %d %q, want 206 \"4567\"", res.StatusCode, body)
	}
	if got := res.Header.Get("Content-Range"); got != "bytes 4-7/16" {
		t.Fatalf("Content-Range = %q", got)
	}
	// no-cache, not no-store: the browser may keep the record store and revalidate
	// it, so a rebuild is still visible on reload without re-downloading megabytes
	// of artifacts on every navigation.
	if got := res.Header.Get("Cache-Control"); got != "no-cache" {
		t.Fatalf("Cache-Control = %q, want no-cache", got)
	}

	// A plain GET stays a full 200.
	full, err := srv.Client().Get(srv.URL + "/browse-index.rrs")
	if err != nil {
		t.Fatal(err)
	}
	defer full.Body.Close()
	all, _ := io.ReadAll(full.Body)
	if full.StatusCode != http.StatusOK || len(all) != 16 {
		t.Fatalf("full response = %d, %d bytes", full.StatusCode, len(all))
	}
}
