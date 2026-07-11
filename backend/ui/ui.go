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

// brandPath is the stable route a deployment's LCATD_BRAND_CSS stylesheet
// is served at.
const brandPath = "brand.css"

// Handler serves the embedded SPA with history-API fallback: unknown
// non-asset paths get index.html so client-side routes deep-link.
//
// A non-empty brandCSS (the LCATD_BRAND_CSS file, read at boot)
// is served at /brand.css and linked from index.html at the end of <head>,
// after the built app.css, so its rules win the cascade: a deployment
// re-brands the app.css tokens (or anything else) by authoring plain CSS,
// without forking or rebuilding the SPA. The link is render-blocking like
// any head stylesheet, so the first paint already carries the brand.
func Handler(brandCSS []byte) http.Handler {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		panic(err) // embed shape is fixed at compile time
	}
	index, err := fs.ReadFile(sub, "index.html")
	if err != nil {
		panic(err) // dist always carries an index.html (placeholder or built)
	}
	if len(brandCSS) > 0 {
		link := []byte(`<link id="lcat-brand" rel="stylesheet" href="/` + brandPath + `"></head>`)
		index = bytes.Replace(index, []byte("</head>"), link, 1)
	}
	fileServer := http.FileServer(http.FS(sub))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/")
		if path == brandPath && len(brandCSS) > 0 {
			w.Header().Set("Content-Type", "text/css; charset=utf-8")
			w.Write(brandCSS)
			return
		}
		if path != "" && path != "index.html" {
			if f, err := sub.Open(path); err == nil {
				f.Close()
				fileServer.ServeHTTP(w, r)
				return
			}
		}
		// index.html, "/", and the history-API fallback all serve the
		// (possibly brand-linked) index bytes.
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(index)
	})
}
