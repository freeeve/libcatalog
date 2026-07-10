package main

import (
	"flag"
	"fmt"
	"net/http"
	"os"
)

// runServe serves an already-built static site (tasks/181). The point over
// python http.server is HTTP Range support: the roaringrange WASM reader
// fetches index artifacts with Range requests, which http.server ignores --
// silently breaking client-side browse in local previews. net/http's
// FileServer answers 206 partial content natively, so `lcat build && lcat
// serve` is the whole local loop; unlike `hugo server` it renders nothing
// and starts instantly on a large prebuilt site.
func runServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	dir := fs.String("dir", "public", "built site directory to serve")
	addr := fs.String("addr", "127.0.0.1:8500", "listen address (host:port; use :port to expose beyond localhost)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	info, err := os.Stat(*dir)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is not a directory", *dir)
	}
	fmt.Printf("serving %s on http://%s/ (Range-capable; Ctrl-C to stop)\n", *dir, *addr)
	return http.ListenAndServe(*addr, serveHandler(*dir))
}

// serveHandler is the serve command's handler: a static file server with
// preview-friendly caching disabled, so a rebuild is visible on reload
// instead of the browser replaying yesterday's index artifacts.
//
// A retired Work id is answered from the published redirects.json before the file
// server sees the request (tasks/313): 301 to the survivor, 410 for a tombstone.
// The projector has always emitted that map; until this ran, nothing served it and
// every retired permalink answered a bare 404.
func serveHandler(dir string) http.Handler {
	files := http.FileServer(http.Dir(dir))
	retired := newRedirectTable(dir)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if retired.serveRetired(w, r) {
			return
		}
		files.ServeHTTP(w, r)
	})
}
