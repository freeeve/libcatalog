# 155 -- Persisted workindex snapshot: load on boot, save from memory

Plane 1 baseline of the architecture in [154]. Makes the writable serverless
(Lambda) shape viable: a cold start loads the index from one blob instead of
GETting every grain.

## Scope

The snapshot is the in-memory **projection**, not `catalog.nq`. The index keeps
only `grainEntry{etag, identity, merges, barcodes, summaries}` per grain
(`backend/workindex/workindex.go`) -- serialize exactly that.

1. **`Index.Save(ctx) error`** -- serialize the `grains` map (path ->
   projected entry) to one blob, default `data/workindex.snapshot`
   (configurable; kept out of the `data/works/` prefix the index lists).
   Serializes straight from memory -- no grain reads. Whole-map reserialize is
   fine at this scale; incremental is task 156's feed, not here.
2. **Load on boot** -- `New`/`LoadSnapshot(ctx)` reads the snapshot into
   `grains` before the first `refreshLocked`. The existing ETag-diff refresh
   then costs one `List` + GETs for only the grains changed since the snapshot;
   correctness holds against a stale snapshot (ETag diff re-reads the delta,
   unlisted-path sweep drops deletions).
3. **Offline seed tool** -- a one-shot to build the first snapshot without a
   running server: `lcat workindex-snapshot --blob-dir <dir> [--out
   data/workindex.snapshot]` (cmd/lcat is at repo root, not backend/). Needed
   because otherwise the first Lambda cold start still does the full scan.

## Encoding

gob for v1: zero new deps (matches "prefer fewer dependencies"), self-describing,
tolerant of added fields; version the header. Forward path is RoaringRange RRSR +
`splitset` sharding when object size / compression / sharded-incremental-write
start to matter (roaringrange is already a dependency via `search/`). Do not use
`catalog.nq` as the load source -- it is full RDF the backend cannot cheaply
produce, and it belongs to the public plane (see [154], task 157).

## Guard rails

Missing / corrupt / version-mismatch snapshot -> log and fall back to the full
scan (today's behavior). Never fail boot on a bad snapshot.

## Out of scope

- Cross-container read-your-writes freshness -> **task 156** (change feed).
- Any public-site / `catalog.nq` work -> tasks 157-160.

## Verify

- Cold start against a 48k-grain store with a current snapshot serves
  `/v1/works` in well under the Lambda/API-GW timeout (index-load, not scan).
- Publish -> `Save` -> a fresh process loads the edit from the snapshot.
- Missing/corrupt snapshot boots via full scan.
- Unit tests: round-trip Save/Load equals a freshly-scanned index; stale
  snapshot + ETag-diff converges; corrupt blob falls back.
