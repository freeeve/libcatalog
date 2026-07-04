// Package ui embeds the built cataloging SPA (Svelte, see src/) so lcatd
// serves it as static files -- one deployable, no CORS. The committed
// dist/index.html is a placeholder; `npm run build` (run before `go build`
// in a release) overwrites dist/ with the real app, which go:embed picks up
// at compile time. A deployment may instead host dist/ on its own CDN and
// run lcatd API-only.
package ui

import (
	"bytes"
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed all:dist
var dist embed.FS

// placeholderMarker identifies the committed placeholder index.html; the real
// Vite build does not contain it. See IsPlaceholder.
var placeholderMarker = []byte("lcat-ui-placeholder")

// IsPlaceholder reports whether the embedded SPA is the committed placeholder
// (no build was run before `go build`). Callers log a warning: the API works,
// but the browser UI shows a build notice rather than the app.
func IsPlaceholder() bool {
	data, err := dist.ReadFile("dist/index.html")
	if err != nil {
		return true
	}
	return bytes.Contains(data, placeholderMarker)
}

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
