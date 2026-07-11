package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// pagerSite is the shape every paginated libcat site has: Hugo writes
// /page/N/index.html and never /page/index.html, so /page/ is an index-less
// directory one URL truncation away from a link the home page emits. /search/ is
// the artifact directory, index-less for good and full of large binaries.
func pagerSite(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, p := range []string{"page/1", "page/2", "search", "works"} {
		if err := os.MkdirAll(filepath.Join(dir, p), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write := func(rel, body string) {
		if err := os.WriteFile(filepath.Join(dir, rel), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("index.html", `<html><link rel="stylesheet" href="/lcat.css"><a href="/page/2/">2</a></html>`)
	write("page/1/index.html", "<html>page one</html>")
	write("page/2/index.html", "<html>page two</html>")
	write("works/index.html", "<html>all works</html>")
	write("search/browse-records.bin", "binary-record-store")
	write("search/browse-index.rrs", "binary-index")
	return dir
}

// looksLikeAListing reports whether a body is Go's dirList: a <pre> block of links
// and no stylesheet. Asserting on the shape rather than the status is the point --
// a 404 whose body still enumerated the directory would satisfy a status check.
func looksLikeAListing(body string) bool {
	return strings.Contains(body, "<pre>") || strings.Contains(body, `<a href="`)
}

// . `lcat serve` is the OPAC's server, and no static host --
// S3, CloudFront, GitHub Pages, Netlify, nginx -- lists an index-less directory.
func TestAnIndexLessDirectoryIsNotListed(t *testing.T) {
	srv := httptest.NewServer(serveHandler(pagerSite(t), false))
	defer srv.Close()

	for _, p := range []string{"/page/", "/search/"} {
		code, _, body := get(t, srv, p)
		if code != http.StatusNotFound {
			t.Errorf("GET %s -> %d, want 404: a static host does not list it", p, code)
		}
		if looksLikeAListing(body) {
			t.Errorf("GET %s -> %d but the body still enumerates the directory: %q", p, code, body)
		}
	}

	// The same directory without its trailing slash. FileServer would normally 301
	// to add one; it must not, because there is nothing there to redirect to.
	if code, _, body := get(t, srv, "/page"); code != http.StatusNotFound || looksLikeAListing(body) {
		t.Errorf("GET /page -> %d (listing=%v), want a plain 404", code, looksLikeAListing(body))
	}
}

// The control, and the whole argument: the same server renders every directory that
// does carry an index.html. A listing is the server choosing to list, not the build
// failing to render -- so a handler that 404'd everything would not pass this.
func TestAnIndexBearingDirectoryStillRenders(t *testing.T) {
	srv := httptest.NewServer(serveHandler(pagerSite(t), false))
	defer srv.Close()

	for path, want := range map[string]string{
		"/":        "stylesheet",
		"/works/":  "all works",
		"/page/1/": "page one",
		"/page/2/": "page two",
	} {
		code, _, body := get(t, srv, path)
		if code != http.StatusOK || !strings.Contains(body, want) {
			t.Errorf("GET %s -> %d, want 200 containing %q (body %q)", path, code, want, body)
		}
	}
}

// The files inside an unlisted directory are still served: /search/ is hidden from
// enumeration, not from lcat-browse.js, which fetches these by name.
func TestFilesUnderAnUnlistedDirectoryAreStillServed(t *testing.T) {
	srv := httptest.NewServer(serveHandler(pagerSite(t), false))
	defer srv.Close()

	code, _, body := get(t, srv, "/search/browse-records.bin")
	if code != http.StatusOK || body != "binary-record-store" {
		t.Errorf("GET /search/browse-records.bin -> %d %q, want the file", code, body)
	}
}

// again: no-store made a browse session re-download the record store on
// every navigation. --dev restores it for anyone who wants nothing cached at all.
func TestDevRestoresNoStore(t *testing.T) {
	dir := pagerSite(t)
	for _, tc := range []struct {
		dev  bool
		want string
	}{{false, "no-cache"}, {true, "no-store"}} {
		srv := httptest.NewServer(serveHandler(dir, tc.dev))
		res, err := srv.Client().Get(srv.URL + "/works/")
		if err != nil {
			t.Fatal(err)
		}
		got := res.Header.Get("Cache-Control")
		res.Body.Close()
		srv.Close()
		if got != tc.want {
			t.Errorf("--dev=%v: Cache-Control = %q, want %q", tc.dev, got, tc.want)
		}
	}
}
