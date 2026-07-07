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

**Record encoding: JSON**, shared with the change feed (task 156) -- one
projected-entry shape, one encode/decode + test surface across snapshot and feed.
**v1 container: a single gzipped-JSON blob** (array of `{path, ...entry}`,
version-tagged header). Rationale over gob: portable and inspectable (the offline
seed tool and a broken-snapshot post-mortem stay debuggable; the Rust/WASM side
can read it), stdlib, zero new deps, forgiving schema evolution (unknown fields
ignored, missing zero) -- gob is a Go-only, opaque stream meant for ephemeral RPC,
not durable storage that must survive binary upgrades. Give the projection types
(`GrainIdentity`/`WorkIdentity`/`InstanceIdentity`/`Merge`) explicit json tags so
a Go field rename is a deliberate, visible schema change.

**Forward path (container only): RoaringRange RRSR**, wrapping the *same* JSON
record payloads with a zstd shared dictionary, adopted only if object
size/compression or range-served reads (Level B) start to matter (roaringrange is
already a dependency via `search/`). This buys **compression + range access, not
sharded writes**: `splitset` shards RRS/RRTI *search* bodies, not RRSR records, so
it does not apply to the snapshot. The admin plane's incremental-write mechanism is
snapshot(base) + feed(delta) + fold (task 156); `splitset` base+delta is the
*public* search index's job (tasks 158/159). Because the snapshot is a disposable,
rebuildable-from-scan cache, JSON-blob -> RRSR is a `Save`/`Load` change plus a
re-seed, not a data migration -- no lock-in from starting simple.

## Why not catalog.nq

Measured on the target corpus (queerbooks, 48,515 works): `catalog.nq` is
**~448 MB** of full-RDF N-Quads (~9.5 KB/work). The projection this snapshot
stores is a subset of the ~99 MB in-memory index -- roughly tens of MB gzipped,
~15-40x smaller on the wire. Beyond size, `catalog.nq` would have to be
**full-parsed and re-derived** (`ParseNQuads` -> `Dataset` -> `Scan`/`Summarize`)
on every cold start, versus a plain JSON unmarshal here; the backend **cannot
produce it from memory** (it holds only the projection, not the full RDF), so it
can't self-maintain it; and its merged shape has **no per-grain delta**, so it
can't back the feed's read-your-writes (task 156). `catalog.nq` is the public
plane's artifact (task 157), not the admin index's.

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
