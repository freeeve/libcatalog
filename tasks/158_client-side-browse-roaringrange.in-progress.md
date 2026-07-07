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
2. **Wire the WASM reader in Hugo.** Ship `roaringrange_reader` (wasm + JS glue)
   as a Hugo asset; boot `RrsCatalog.openAll(...)` (or `RrtIndex` +
   `RrfFacets` + `RrsRecords` for the term path) in the browse shell; render
   search results, facet counts, pagination (`RrsCursor`), and detail cards from
   the record store -- replacing the interim substring filter (`lcat-search.js`,
   an explicit stopgap) and reusing the existing facet UI (`lcat-facets.js`).
   The `<noscript>`/full-list path stays as progressive-enhancement fallback.
3. **Term vs trigram:** the reader supports both; keep the build's per-language
   routing (RRTI term for segmented, RRS trigram for unsegmented) and open the
   matching reader class per the manifest.

## Verify

- Browse shell performs search, applies facet filters with correct counts, pages
  results, and opens details -- all client-side, no backend call beyond static
  asset + range fetches.
- Works against a `splitset`-sharded index without downloading whole shards.
