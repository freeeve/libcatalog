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
- `splitset` sharding so indexes are range-fetched, not whole-downloaded, into
  the millions.

Live availability stays client-side (already is -- keyed by Reserve ID, never in
the graph), so it composes here.

## Cross-repo dependency

Confirm the RoaringRange **reader** (Rust/WASM) exposes facet + search + record
query APIs to JS -- the build side (`facets.go`, `records.go`, `lookup.go`) is
present; verify the reader surface. If a reader API is missing, file a
roaringrange task (leave it uncommitted there per the cross-repo convention) and
note the version bump here.

## Verify

- Browse shell performs search, applies facet filters with correct counts, pages
  results, and opens details -- all client-side, no backend call beyond static
  asset + range fetches.
- Works against a `splitset`-sharded index without downloading whole shards.
