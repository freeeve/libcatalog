# Reader-path E2E (tasks/158)

Real-browser verification of the client-side RoaringRange browse: `run.sh`
builds the exampleSite with `[params.search] engine = "roaringrange"`, emits
the search/browse artifacts from `fixture-catalog.json` via `lcat index`,
serves the result over `range-server.mjs` (a static server that honors HTTP
Range -- the reader requires that, and `python -m http.server` does not), and
runs `browse.spec.mjs` in headless Chromium: facet panel, facet-only browse,
query+facet intersection, facet-excludes-hit, static-list restore (5 checks).

This exists because jsdom (`npm run test:js`) cannot execute ES modules/WASM,
so nothing else covers the reader path. Playwright is deliberately NOT a
package.json dependency; provide it via `PLAYWRIGHT_PKG` (see `run.sh` header)
or a local install, and `npx playwright install chromium` once.

The fixture ids (wexampleone/two/three) mirror the exampleSite works so result
cards link to real detail pages; the spec's expectations encode the fixture
(one `ebook` work: wexampletwo).
