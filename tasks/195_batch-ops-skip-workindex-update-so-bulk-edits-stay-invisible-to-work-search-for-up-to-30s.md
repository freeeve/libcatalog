# 195 -- batch ops skip workindex update so bulk edits stay invisible to work search for up to 30s

Filed from libcat-e2e on 2026-07-09 (cross-repo ask).

## Symptom

`POST /v1/batch/ops` with `dryRun:false` reports success (`applied:2,
added:2, failed:0`, per-record etags) but the edit is not visible to
`GET /v1/works` -- its results, its facet counts, and its `?tag=` filter --
for up to `workindex.DefaultTTL` (30s). The identical edit applied through
the single-record path is visible in ~1ms.

Measured on the 8481 playground (`harness/freshness2.py` in libcat-e2e),
same tag, same work, back to back:

| path | endpoint | visible after |
|---|---|---|
| single record | `POST /v1/works/{id}/ops` | **1 ms** |
| batch | `POST /v1/batch/ops` | **16.8 s** / **28.3 s** (two runs) |

Repeated runs land anywhere in 0--30s depending on where the write falls in
the TTL window, so it reads as flaky rather than as a fixed delay.

## Root cause

The single-record write path updates the index for the process's own write:

- `backend/httpapi/records_handlers.go:123` `ix.Apply(bibframe.GrainPath(workID), newTag, updated)`
- `backend/httpapi/records_handlers.go:126` `ix.AppendFeed(ctx, bibframe.GrainPath(workID))`
- same pair again at `:206` and `:418`

`batch.Service.Run` (`backend/batch/batch.go:184`) CAS-writes each grain and
then audits/notifies, but never calls `ix.Apply` / `ix.AppendFeed` -- the
package does not reference `workindex` at all beyond a comment at
`batch/batch.go:109`. So batch writes fall back to the `refreshLocked` List-diff
backstop, gated by `workindex.DefaultTTL = 30 * time.Second`
(`backend/workindex/workindex.go:32`).

That contradicts the contract stated on the very helpers the single-record path
uses -- `workindex.go:154`: *"exact for the process's own writes without waiting
out the TTL"*, and `:133`: *"keeps the index exact without waiting out the TTL"*.

## Why it matters

- A cataloger runs a bulk edit, then searches for what they just changed and
  finds nothing. No spinner, no "indexing", no staleness indicator -- the list
  simply shows pre-edit data. This is the single worst ergonomics failure found
  in the admin UI so far: the tool silently lies about the state of the catalog.
- Worse than cosmetic: a **subsequent batch selection resolves against the stale
  index** (`batch.Service.Resolve` -> search selection -> workindex). Chaining
  two batch runs inside the TTL window can therefore select the wrong target set
  -- e.g. "add tag X" then "select tag X, set field Y" silently matches nothing.
- The 30s backstop exists for writes made by *other* containers; it should never
  be the mechanism by which this process sees its own writes.

## Expected

After `POST /v1/batch/ops` returns, `GET /v1/works` (results, facets, `?tag=`)
reflects every applied record, with no TTL wait -- matching the single-record
path.

## Suggested fix

Give `batch.Service` the same index handle the record handlers hold and, for
each successfully CAS-written grain, call `ix.Apply(path, newEtag, grain)` plus
`ix.AppendFeed(ctx, path)`. The per-record loop in `Run` already has the grain
bytes and the new etag it needs (they are what it reports back in
`RunResult.results[].etag`). Skip both on `dryRun`.

## Repro

```sh
# libcat-e2e
python3 harness/freshness2.py
# [batch]  POST /v1/batch/ops -> 200 applied=2
# [batch]  visible after 16834 ms
# [single] POST /v1/works/{id}/ops -> 200
# [single] visible after 1 ms
```

Note the single-record path requires an `If-Match` etag (returns 428 without
one); the batch path requires none.
