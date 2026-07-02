// Package ui embeds the built cataloging SPA (Svelte, see src/) so lcatd
// serves it as static files -- one deployable, no CORS. The committed
// dist/index.html is a placeholder; `npm run build` (run before `go build`
// in a release) overwrites dist/ with the real app, which go:embed picks up
// at compile time. A deployment may instead host dist/ on its own CDN and
// run lcatd API-only.
package ui

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed all:dist
var dist embed.FS

// Handler serves the embedded SPA with history-API fallback: unknown
// non-asset paths get index.html so client-side routes deep-link.
func Handler() http.Handler {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		panic(err) // embed shape is fixed at compile time
	}
	fileServer := http.FileServer(http.FS(sub))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/")
		if path != "" {
			if f, err := sub.Open(path); err == nil {
				f.Close()
				fileServer.ServeHTTP(w, r)
				return
			}
		}
		// History-API fallback.
		r.URL.Path = "/"
		fileServer.ServeHTTP(w, r)
	})
}
