package main

import (
	"flag"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path"
)

// runServe serves an already-built static site.
//
// It is not only a preview server. specifies it as the OPAC's server --
// "--addr :8502 exposes it ... queerbooks can drop scripts/opac-server.go on
// adoption" -- and both demo catalogs run it that way. A doc comment that said
// "local preview" is how a directory listing came to read as acceptable
// . It serves readers.
//
// The point over python http.server is HTTP Range support: the roaringrange WASM
// reader fetches index artifacts with Range requests, which http.server ignores --
// silently breaking client-side browse. net/http's FileServer answers 206 partial
// content natively, so `lcat build && lcat serve` is the whole local loop; unlike
// `hugo server` it renders nothing and starts instantly on a large prebuilt site.
func runServe(args []string) error {
	flags := flag.NewFlagSet("serve", flag.ExitOnError)
	dir := flags.String("dir", "public", "built site directory to serve")
	addr := flags.String("addr", "127.0.0.1:8500", "listen address (host:port; use :port to expose beyond localhost)")
	dev := flags.Bool("dev", false, "send Cache-Control: no-store instead of no-cache (nothing is written to the browser cache at all)")
	if err := flags.Parse(args); err != nil {
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
	return http.ListenAndServe(*addr, serveHandler(*dir, *dev))
}

// noDirList is an http.FileSystem that reports a directory with no index.html as
// absent, so http.FileServer renders its own 404 instead of generating a listing.
//
// Go's FileServer lists such a directory, and there is no flag for it. No static
// host does: S3 website endpoints, CloudFront, GitHub Pages, Netlify and nginx
// (whose autoindex defaults to off) all answer 403 or 404. A preview whose one job
// is to show what the published site will be must not answer 200 where the host
// answers 404 -- it hides exactly the URL-shape bugs it exists to surface. A Dewey
// number "813/.6" mints /classifications/813/.6/, leaving /classifications/813/
// index-less; under a listing that broken ancestor looked like a working page all
// the way into a published catalog.
//
// It reaches readers too. Hugo emits /page/N/index.html and never /page/index.html,
// so every paginated libcat site has an index-less /page/ one URL truncation away
// from a link its own home page emits.
type noDirList struct{ fs http.FileSystem }

func (n noDirList) Open(name string) (http.File, error) {
	f, err := n.fs.Open(name)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	if info.IsDir() {
		index, err := n.fs.Open(path.Join(name, "index.html"))
		if err != nil {
			f.Close()
			return nil, fs.ErrNotExist
		}
		index.Close()
	}
	return f, nil
}

// serveHandler is the serve command's handler: a static file server that lists no
// directories, answers a retired Work id from the published redirect map, and
// revalidates rather than caching blind.
//
// Caching. `no-cache` does not mean "do not cache" -- it means "cache, but
// revalidate before reuse", which FileServer answers with a 304 off Last-Modified.
// A rebuild is still visible on the next reload, and the 9.9MB record store the
// browse reader fetches is no longer re-downloaded on every navigation, as
// `no-store` made it do. It is not `max-age` because serve cannot tell
// a fingerprinted asset, which may be cached forever, from an index.html, which may
// not. `--dev` restores `no-store` for anyone who wants nothing written to the
// browser cache at all.
//
// A retired Work id is answered from the published redirects.json before the file
// server sees the request: 301 to the survivor, 410 for a tombstone.
func serveHandler(dir string, dev bool) http.Handler {
	files := http.FileServer(noDirList{http.Dir(dir)})
	retired := newRedirectTable(dir)
	cache := "no-cache"
	if dev {
		cache = "no-store"
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", cache)
		if retired.serveRetired(w, r) {
			return
		}
		files.ServeHTTP(w, r)
	})
}
