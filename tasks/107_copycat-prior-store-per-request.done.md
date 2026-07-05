# 107 -- copycat: Stage and Commit each load the entire prior grain store

> Filed from the 2026-07-05 full-code review.

## Symptom

Staging or committing a single copy-cataloged record costs O(corpus): every work
grain is listed, fetched, and scanned -- twice across a stage-then-commit flow,
plus a third full listing for the pre-commit snapshot.

## Cause

`matchRecords` (backend/copycat/copycat.go:463-464) calls
`bibframe.LoadPriorStore(ctx, s.Blob, s.Prefix+"data/works/", s.feed())` and
`identity.SeedResolver(r, prior.Grains)` on every call; `Stage` calls it once and
`Commit` calls it again (copycat.go:650). `LoadPriorStore`
(bibframe/reingest_store.go:20-62) Gets and scans every `*.nq` grain including
editorial bytes. `preCommitSnapshot` (copycat/revert.go:66-74) separately lists
the whole `data/works/` tree on every Commit.

## Fix sketch

Reuse one loaded prior/resolver between Stage and Commit (cache keyed by store
generation, or persist the staged match against the batch), and share the
identity index proposed in [[106_httpapi-per-request-corpus-scans]] once it
exists -- copycat only needs provider keys and cluster keys, not editorial
bytes. Scope preCommitSnapshot's listing to the works the batch touches.

## Acceptance

- A stage-then-commit of one record against a large seeded store does not read
  every grain twice; measured blob reads drop from O(2N+) to O(N) or better
  (O(1) with the shared index).

## Status (2026-07-05 session)

Done. `copycat.Service` gained an `Index *workindex.Index` field (appdeps
wires the shared one; guarded to `Prefix == ""` since the index covers
repo-layout paths):

- `matchRecords` seeds its throwaway resolver from `Index.SeedResolver`
  (grains in path order, then merges -- same order as the old
  LoadPriorStore pass; the index now also scans lcat:mergedInto per grain
  via the new `bibframe.ScanMergesDataset`) instead of reading and parsing
  every grain per Stage/Commit. Nil index falls back to LoadPriorStore.
- `Commit` calls `Index.RefreshNow` (TTL-bypassing ETag diff -- a stat-cached
  List, not a corpus read) before its re-match, since match accuracy drives
  the overlay policy; after `ingest.RunStore` it pushes the changed grains
  back in via `Index.Update`, so the editor's duplicate/barcode checks see
  committed records immediately. `Revert` does the same for restored grains.
- `preCommitSnapshot` takes its existing-path set from `Index.GrainPaths`
  (fresh from the RefreshNow moments earlier) instead of a third full
  listing; the fallback keeps the live List.

The copycat test suite now runs with the index wired (as appdeps deploys
it), and `TestIndexedMatchEqualsFallbackAndSkipsCorpusReads` pins both
equivalence with the LoadPriorStore fallback and the zero-grain-reads
warm-stage acceptance. The pipeline inside `ingest.RunStore` still does its
own LoadPriorStore per commit (it needs editorial bytes and CAS etags the
index deliberately doesn't carry) -- so a stage-then-commit dropped from
three full corpus loads plus a listing to one (inside RunStore), and Stage
alone from one to zero.
