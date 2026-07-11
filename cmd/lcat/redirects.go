package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/freeeve/libcat/project"
)

// goneBody is what a tombstoned Work id answers with. Short on purpose: a 410 is
// the answer, and the body only has to explain it to a human who arrived by
// following an old link.
const goneBody = `<!DOCTYPE html>
<html lang="en">
<head><meta charset="utf-8"><title>410 Gone</title><meta name="robots" content="noindex"></head>
<body><h1>Gone</h1><p>This record was retired from the catalog and has no successor.</p></body>
</html>
`

// redirectTable is the served view of the projector's redirects.json:
// retired Work id -> surviving Work id, empty when the id was tombstoned with no
// successor.
//
// It is re-read whenever the file's size or mtime changes rather than loaded once,
// because `lcat serve` is a preview server for a tree that is rebuilt under it --
// it reads every other file from disk per request, and a redirect map pinned at
// startup would answer yesterday's merges until someone restarted it.
type redirectTable struct {
	path string

	mu     sync.Mutex
	to     map[string]string
	mod    time.Time
	size   int64
	loaded bool
	warned bool
}

// newRedirectTable binds a table to dir/redirects.json. Nothing is read yet, and a
// missing file is not an error: a site whose catalog has retired nothing publishes
// no map, and every id it serves is live.
func newRedirectTable(dir string) *redirectTable {
	return &redirectTable{path: filepath.Join(dir, "redirects.json")}
}

// lookup reports the entry for a retired Work id. ok is false for a live id.
func (t *redirectTable) lookup(id string) (to string, ok bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.refresh()
	to, ok = t.to[id]
	return to, ok
}

// refresh reloads the map when the file appears, changes or vanishes. Caller holds
// the lock.
func (t *redirectTable) refresh() {
	fi, err := os.Stat(t.path)
	if err != nil {
		t.to, t.loaded = nil, false
		return
	}
	if t.loaded && fi.ModTime().Equal(t.mod) && fi.Size() == t.size {
		return
	}
	t.mod, t.size, t.loaded = fi.ModTime(), fi.Size(), true

	raw, err := os.ReadFile(t.path)
	if err != nil {
		t.to = nil
		return
	}
	var m project.RedirectMap
	if err := json.Unmarshal(raw, &m); err != nil {
		// Keep serving files rather than dying on a half-written artifact: a
		// rebuild rewrites this file while requests are in flight.
		fmt.Fprintf(os.Stderr, "serve: %s is not a redirect map, ignoring it: %v\n", t.path, err)
		t.to = nil
		return
	}
	if m.Version != project.SchemaVersion && !t.warned {
		// Warn, but serve it. The from/to pair is the whole contract here, and it
		// has not moved; the version gates catalog.json's shape, which this file
		// does not carry. Refusing the map would answer 404 -- the bug this fixes.
		fmt.Fprintf(os.Stderr, "serve: %s has schema version %d, this lcat targets %d\n", t.path, m.Version, project.SchemaVersion)
		t.warned = true
	}
	t.to = make(map[string]string, len(m.Redirects))
	for _, r := range m.Redirects {
		t.to[r.From] = r.To
	}
}

// splitWorkPath decomposes a request path into the prefix that precedes the works
// section and the Work id, for /works/<id> and /works/<id>/ alike.
//
// The prefix is kept rather than assumed empty so that a multilingual site's
// /es/works/<id>/ redirects to /es/works/<survivor>/ and not out of the reader's
// language. Anything deeper than the id (/works/<id>/index.html) is left to the
// file server: it is a request for a file, not for the Work's page.
func splitWorkPath(p string) (prefix, id string, ok bool) {
	parts := strings.Split(strings.Trim(p, "/"), "/")
	if len(parts) < 2 || parts[len(parts)-2] != "works" {
		return "", "", false
	}
	id = parts[len(parts)-1]
	if id == "" {
		return "", "", false
	}
	return "/" + strings.Join(parts[:len(parts)-2], "/"), id, true
}

// validWorkID reports whether s is safe to interpolate into a Location header.
//
// Work ids are opaque tokens the projector mints, so this should always hold. It
// is checked anyway because the value crosses a trust boundary the moment it is
// read back off disk: a `to` of "https://example.net" or one carrying a newline
// would turn a redirect map into an open redirect or a header injection. A `to`
// that fails is treated as no successor -- 410 rather than a wrong 301.
func validWorkID(s string) bool {
	if s == "" || len(s) > 128 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '-', r == '_', r == '.':
		default:
			return false
		}
	}
	return true
}

// serveRetired answers a request for a retired Work id, reporting whether it did.
//
// A merge answers 301 to the survivor; a tombstone answers 410. The check runs
// before the file server, so the meta-refresh stub the Hugo module generates for a
// merged id -- which exists for hosts that can serve nothing but files -- is
// upgraded here to the real status code. Both say the same thing; one says it to a
// crawler.
func (t *redirectTable) serveRetired(w http.ResponseWriter, r *http.Request) bool {
	prefix, id, ok := splitWorkPath(r.URL.Path)
	if !ok {
		return false
	}
	to, retired := t.lookup(id)
	if !retired {
		return false
	}
	if validWorkID(to) {
		http.Redirect(w, r, path.Join(prefix, "works", to)+"/", http.StatusMovedPermanently)
		return true
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusGone)
	if r.Method != http.MethodHead {
		io.WriteString(w, goneBody)
	}
	return true
}
