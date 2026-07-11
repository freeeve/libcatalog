package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/freeeve/libcat/project"
)

// retiredSite builds a served tree: one live Work page, one meta-refresh stub for a
// merged id (the Hugo module emits these for static hosts), and a redirects.json
// naming a merge and a tombstone.
func retiredSite(t *testing.T, entries ...project.Redirect) string {
	t.Helper()
	dir := t.TempDir()
	for _, p := range []string{"works/wlive", "works/wmerged"} {
		if err := os.MkdirAll(filepath.Join(dir, p), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write := func(rel, body string) {
		if err := os.WriteFile(filepath.Join(dir, rel), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("works/wlive/index.html", "<html>live work</html>")
	write("works/wmerged/index.html", `<meta http-equiv="refresh" content="0; url=/works/wlive/">`)
	writeRedirects(t, dir, project.SchemaVersion, entries...)
	return dir
}

func writeRedirects(t *testing.T, dir string, version int, entries ...project.Redirect) {
	t.Helper()
	if entries == nil {
		entries = []project.Redirect{}
	}
	raw, err := json.Marshal(project.RedirectMap{Version: version, Redirects: entries})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "redirects.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
}

// get issues a request without following redirects, so the status and Location are
// the server's own answer.
func get(t *testing.T, srv *httptest.Server, path string) (int, string, string) {
	t.Helper()
	req, err := http.NewRequest("GET", srv.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := srv.Client().Transport.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	return res.StatusCode, res.Header.Get("Location"), string(body)
}

// . The promise in ARCHITECTURE.md:103: "a merge or split must leave a
// redirect/tombstone so shared links and SEO survive". Both arms, and a live id
// beside them -- a server that redirected everything would pass an assertion that
// only looked at the retired ids.
func TestServeAnswersRetiredWorkIDs(t *testing.T) {
	dir := retiredSite(t,
		project.Redirect{From: "wmerged", To: "wlive"},
		project.Redirect{From: "wgone", To: ""},
	)
	srv := httptest.NewServer(serveHandler(dir, false))
	defer srv.Close()

	if code, loc, _ := get(t, srv, "/works/wmerged/"); code != http.StatusMovedPermanently || loc != "/works/wlive/" {
		t.Errorf("merged id -> %d Location=%q, want 301 /works/wlive/", code, loc)
	}
	if code, _, body := get(t, srv, "/works/wgone/"); code != http.StatusGone || !strings.Contains(body, "no successor") {
		t.Errorf("tombstoned id -> %d, want 410 with an explanatory body (body %q)", code, body)
	}
	// Control: a live Work still serves its page, and an unknown id is still a 404.
	if code, _, body := get(t, srv, "/works/wlive/"); code != http.StatusOK || !strings.Contains(body, "live work") {
		t.Errorf("live work -> %d %q, want 200 with its page", code, body)
	}
	if code, _, _ := get(t, srv, "/works/wneverexisted/"); code != http.StatusNotFound {
		t.Errorf("unknown id -> %d, want 404: an id nobody retired is not a redirect", code)
	}
}

// The stub the module writes for static hosts must not shadow the 301. A host that
// can only serve files answers 200 + meta refresh; a host that can do better does.
func TestTheRedirectBeatsTheStubOnDisk(t *testing.T) {
	dir := retiredSite(t, project.Redirect{From: "wmerged", To: "wlive"})
	srv := httptest.NewServer(serveHandler(dir, false))
	defer srv.Close()

	code, loc, _ := get(t, srv, "/works/wmerged/")
	if code != http.StatusMovedPermanently || loc != "/works/wlive/" {
		t.Fatalf("a merged id with a stub on disk -> %d Location=%q, want the 301", code, loc)
	}
	// Control: take the map away and the very same path serves the stub, 200. So the
	// 301 above is the map winning over a file that is there -- not a missing file
	// 404ing, and not a path the file server never had an answer for.
	if err := os.Remove(filepath.Join(dir, "redirects.json")); err != nil {
		t.Fatal(err)
	}
	code, _, body := get(t, srv, "/works/wmerged/")
	if code != http.StatusOK || !strings.Contains(body, "refresh") {
		t.Errorf("with the map gone, the stub -> %d, want 200 with its meta refresh (body %q)", code, body)
	}
}

// A multilingual site's /es/works/<id>/ must land on the Spanish survivor, not on
// the default language's.
func TestARedirectStaysInTheReadersLanguage(t *testing.T) {
	dir := retiredSite(t, project.Redirect{From: "wmerged", To: "wlive"})
	srv := httptest.NewServer(serveHandler(dir, false))
	defer srv.Close()

	if code, loc, _ := get(t, srv, "/es/works/wmerged/"); code != http.StatusMovedPermanently || loc != "/es/works/wlive/" {
		t.Errorf("/es/works/wmerged/ -> %d Location=%q, want 301 /es/works/wlive/", code, loc)
	}
	if code, loc, _ := get(t, srv, "/works/wmerged"); code != http.StatusMovedPermanently || loc != "/works/wlive/" {
		t.Errorf("an unslashed id -> %d Location=%q, want the same 301", code, loc)
	}
}

// `to` is read back off disk and interpolated into a Location header. Work ids are
// opaque tokens, so this never fires in practice -- which is exactly why it is
// asserted: an absolute URL there is an open redirect, and a newline is a header
// injection. A `to` that is not an id means no successor, so: 410, never a wrong 301.
func TestAnUnusableSuccessorIsGoneNotAnOpenRedirect(t *testing.T) {
	for _, bad := range []string{"https://example.net/phish", "//example.net", "../../etc/passwd", "w1\r\nX-Injected: 1", "w/1"} {
		dir := retiredSite(t, project.Redirect{From: "wmerged", To: bad})
		srv := httptest.NewServer(serveHandler(dir, false))
		code, loc, _ := get(t, srv, "/works/wmerged/")
		srv.Close()
		if code != http.StatusGone || loc != "" {
			t.Errorf("to=%q -> %d Location=%q, want 410 and no Location", bad, code, loc)
		}
	}
	// Control: a well-formed id in the same position does redirect, so the test above
	// is measuring the validity check and not a table that never redirects.
	dir := retiredSite(t, project.Redirect{From: "wmerged", To: "wlive"})
	srv := httptest.NewServer(serveHandler(dir, false))
	defer srv.Close()
	if code, _, _ := get(t, srv, "/works/wmerged/"); code != http.StatusMovedPermanently {
		t.Fatalf("a valid successor -> %d, want 301", code)
	}
}

// `lcat serve` reads the built tree from disk on every request, so a rebuild is
// visible on reload. The redirect map has to obey the same rule, or a merge made
// after the server started answers 404 until someone restarts it.
func TestTheMapIsRereadWhenTheBuildChanges(t *testing.T) {
	dir := retiredSite(t)
	srv := httptest.NewServer(serveHandler(dir, false))
	defer srv.Close()

	if code, _, _ := get(t, srv, "/works/wgone/"); code != http.StatusNotFound {
		t.Fatalf("before the merge, an unknown id -> %d, want 404", code)
	}
	// Stat granularity is coarse enough that a same-second rewrite of the same size
	// could look unchanged; the sizes differ here, and the sleep costs nothing.
	time.Sleep(10 * time.Millisecond)
	writeRedirects(t, dir, project.SchemaVersion, project.Redirect{From: "wgone", To: ""})

	if code, _, _ := get(t, srv, "/works/wgone/"); code != http.StatusGone {
		t.Errorf("after the rebuild, the tombstone -> %d, want 410 without a restart", code)
	}
}

// A catalog that has retired nothing publishes no map, and a half-written one is
// rewritten under the server on every rebuild. Neither may take the site down.
func TestAMissingOrCorruptMapServesTheSiteAnyway(t *testing.T) {
	dir := retiredSite(t)
	if err := os.Remove(filepath.Join(dir, "redirects.json")); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(serveHandler(dir, false))
	defer srv.Close()
	if code, _, _ := get(t, srv, "/works/wlive/"); code != http.StatusOK {
		t.Errorf("with no map, a live work -> %d, want 200", code)
	}

	if err := os.WriteFile(filepath.Join(dir, "redirects.json"), []byte("{\"version\":12,\"redi"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, _, _ := get(t, srv, "/works/wlive/"); code != http.StatusOK {
		t.Errorf("with a truncated map, a live work -> %d, want 200", code)
	}
}
