# 158 -- Client-side browse / search / facets over RoaringRange

Plane 2 dynamic surface of [154]. The combinatorial views task 157 stopped
pre-rendering are computed in the browser instead, over the RoaringRange WASM
reader -- what it was built for ("search -> details runs client-side over HTTP
Range with no backend", RECORDS.md).

## Scope

Wire the browse shell (task 157) to the RoaringRange reader for:

- **Search** -- term index (`.rrt`) for segmented scripts, trigram (`.rrs`,
  RRSI) for unsegmented; the manifest already routes per language (`search/`).
- **Facets + counts** -- the `facets` arm; bitmap intersect/count is cheap and
  range-fetched.
- **Paged, sorted listing** -- over the RRSR record store (`.bin`/`.idx`) by doc
  ID / rank.
- **Details on demand** -- RRSR `Get(docID)` for a row's fields without a page
  load.
- `splitset` base splits + per-query **pruning** (read only the splits that can
  match) + range-fetch, so indexes are not whole-downloaded, into the millions;
  the reader merges any **delta splits** from recent publishes (task 159).

Live availability stays client-side (already is -- keyed by Reserve ID, never in
the graph), so it composes here.

## Dependency status: READY (verified 2026-07-07) -- this is libcatalog-only

Both sides of the RoaringRange toolchain already exist, so 158 has **no
cross-repo blocker**:

- **Reader (WASM/JS, `rust/pkg/roaringrange.d.ts`):** `RrtIndex`/`RrtHits`
  (term/RRTI -- the build's default path), `RrsIndex`/`RrsCatalog` (trigram),
  `RrfFacets`, `RrsRecords` (`get`/`getMany`/`getText`), `RrssIndex` (splitset,
  for task 159), `RrsLookup`, `RrsCursor` (paged + `facetCountsJson`). The
  combined `RrsCatalog.openAll(index_url, facets_url, records_idx_url,
  records_bin_url)` + `search(query, offset, len, max_missing, filters_json)`
  returns `{ ids, records, facetCounts }` in one call -- search + facet counts +
  record details over HTTP Range.
- **Go builders (roaringrange):** `WriteFacets` (RRSF), `WriteRecords` /
  `WriteRecordsZstd` (RRSR), `WriteLookup` (RRIL) -- present.

### The gap 158 must close (all in libcatalog)

1. **[DONE] Build emits the reader's inputs.** `search/browse.go`
   (`BuildBrowse`, wired into `lcat index`) emits, over a single global doc-id
   space dense from 0 in catalog order: `browse-facets.rrsf` (`WriteFacets` over
   language/format/subject/tag/classification/contributor),
   `browse-records.bin`/`.idx` (`WriteRecords` over the per-Work result-card
   JSON), and `browse-docs.json` (doc-id -> Work-id, so per-language search hits
   bridge into this space and cards link to `/works/<id>`). The per-language
   search indexes keep their own doc spaces; the client bridges via the Work id.
   (RRIL identifier lookup deferred -- not needed for browse/facets/details.)
2. **[DONE] Wire the WASM reader in Hugo.** Done: the build also emits a
   global trigram index `browse-index.rrs` (aligned with the global facets +
   records doc space) so `RrsCatalog.openAll` ties all three together in one
   space; `roaringrange_reader` (wasm + JS glue) is vendored at
   `hugo/static/lcat/`; `hugo/assets/lcat-browse.js` (loaded as a module when
   `[params.search] engine = "roaringrange"`) boots `RrsCatalog.openAll(...)` and
   replaces the results list with ranked search over the reader, restoring the
   static list when the query clears and falling back silently if the reader or
   artifacts are missing. Verified: Hugo builds (default byte-identical;
   roaringrange wires the module + publishes the reader), JS syntax, existing JS
   tests, a11y. **Not yet done:** facet *filtering* (pass `filters_json` from the
   sidebar) and facet-only browse -- these ride the facet-sidebar rework in
   task 157, and the reader already returns `facetCounts` for when it lands. The
   deployment publishes the `lcat index` artifacts at `[params.search] base`
   (default `/search`). Vendored build: the **full** `roaringrange` reader
   (RrsCatalog + RrfFacets + records; the slim `roaringrange_reader` lacks the
   facet API). **Browser-verified with Playwright/Chromium**: typing a query
   boots the reader, searches, and renders record cards linking to the static
   detail pages; clearing restores the static list; no console errors. (Trigram
   relevance is broad at tiny corpus size -- a ranking-tuning follow-up, and the
   per-language RRTI indexes remain the better-relevance path.)

   **Facet filtering + facet-only browse: DONE.** `lcat-browse.js` opens
   `RrfFacets` (meta-only boot) + `RrsRecords` alongside `RrsCatalog`, renders a
   facet panel (fields + full-corpus counts, top-40 categories by count) into
   the `#lcat-browse-facets` host `list.html` emits when the engine is on, and
   serves three read paths over the one shared doc space: query ->
   `catalog.search(q,...,[])`; query+facets -> `catalog.search(q,...,filters)`;
   facets-only -> `RrfFacets.filterIds(allIds, filters)` + `records.getMany`.
   Playwright/Chromium E2E (5/5): panel renders from the sidecar; facet-only
   ebook returns exactly the right work; query+facet intersects; a facet
   excludes a matching text hit; clearing query+facets restores the static list
   byte-for-byte. Default (pagefind) build stays byte-identical; a11y clean
   (117 pages).

   Verification note: the reader requires an HTTP-Range-capable origin (S3/CDN/
   nginx/`hugo server` all qualify; **python http.server does not** -- symptoms
   are "range bytes=0-15 returned N bytes" and a clean fallback to the static
   list). The E2E harness is committed at `hugo/e2e/` (`run.sh` builds the
   exampleSite + artifacts, serves over a Range-capable server, and drives the
   5 checks in headless Chromium; see its README for the Playwright setup).

   Follow-ups riding later tasks: facet display labels + i18n of field names
   (157 sidebar rework), pagination beyond the first PAGE=60 (RrsCursor),
   ranking tuning / per-language stemmed path, splitset delta merge (159).
3. **Term vs trigram:** the client browse uses the global trigram index (language
   -agnostic, one doc space with facets/records). The per-language RRTI/RRS
   indexes (search.go) stay for a future stemmed-search refinement, bridged via
   `browse-docs.json` (doc id -> Work id).

## Verify

- Browse shell performs search, applies facet filters with correct counts, pages
  results, and opens details -- all client-side, no backend call beyond static
  asset + range fetches.
- Works against a `splitset`-sharded index without downloading whole shards.
