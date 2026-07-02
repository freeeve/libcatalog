# 027 -- Blob store interface (`storage/blob`)

## Context

The Tier 2 dynamic module (plan: Tier 2 Dynamic Cataloging Module) targets BIBFRAME
grains in S3-compatible object storage. The existing `storage.Sink` is write-only
(`Create(path)`), which suits build pipelines but not read-modify-write editorial
publishing, which needs Get/List/Delete plus conditional writes for optimistic
concurrency. Sink stays untouched; a fuller `Store` lands as a sibling package,
stdlib-only so the core module's dependency tree is unchanged.

## Scope

1. **`storage/blob/blob.go`**: `Store` interface --
   `Get(ctx, path) (data []byte, etag string, err error)`,
   `Put(ctx, path, data, PutOptions) (etag string, err error)`,
   `List(ctx, prefix) iter.Seq2[Entry, error]` (`Entry{Path, ETag, Size}`),
   `Delete(ctx, path) error`.
   `PutOptions{IfMatch string, IfNoneMatch bool, ContentType string}`.
   Sentinel errors `ErrNotFound`, `ErrPreconditionFailed`.
   Optional capability `Signer` -- `SignedGetURL(ctx, path, ttl) (string, error)`
   (implemented later by the S3 store; Dir/Mem do not).
2. **`storage/blob/dir.go`**: `DirStore` over a local directory; etags = sha256 of
   content; conditional Put emulated (documented best-effort, for dev/tests).
3. **`storage/blob/mem.go`**: `MemStore` for tests (mutex-guarded map, exact
   conditional semantics).
4. **`SinkOf(Store) storage.Sink`** adapter so a Store can serve existing Sink
   call sites.

## Acceptance

- A shared conformance test suite exercises both impls: Get/Put/Delete/List
  round-trips, `IfNoneMatch` create-only conflict, `IfMatch` stale-etag rejection,
  etag changes on content change, `ErrNotFound` on missing keys, List prefix
  filtering and ordering.
- `SinkOf` writes appear via `Get`.
- Core `go.mod` unchanged (stdlib only); existing packages untouched.
