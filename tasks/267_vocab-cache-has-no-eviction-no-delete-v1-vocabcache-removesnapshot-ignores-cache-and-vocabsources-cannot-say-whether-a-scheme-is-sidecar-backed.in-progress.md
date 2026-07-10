# 267 -- vocab cache has no eviction: no DELETE /v1/vocabcache, RemoveSnapshot ignores cache/, and vocabsources cannot say whether a scheme is sidecar-backed

Opened 2026-07-09.

Split out of **265** (fixed in v0.103.0). The report that filed 265 raised three
asks "independently of the fix". The fix landed; these did not.

265's urgency is gone: a cached live pick now costs its own size, not its
scheme's, because a sidecar-backed scheme carries a map overlay instead of
replaying its snapshot into resident maps. What remains is an operability gap,
not a capacity bomb.

## What is missing

**No undo for a click.** `POST /v1/vocabcache`
(`backend/httpapi/vocabsources_handlers.go`) is the only route that touches the
live-pick cache. There is no `DELETE`, no eviction, no TTL. `CacheTerm`
(`backend/vocabsrc/cache.go`) writes `cache/<scheme>/<hash>.nq`; removing one
means finding a sha256-named blob by hand. `cachedSchemes` then keeps the scheme
in the reload set permanently, by design.

**`RemoveSnapshot` leaves the cache behind.** `backend/vocabsrc/vocabsrc.go`
deletes the snapshot and its meta and never looks at `cache/<scheme>/`.
Uninstalling and reinstalling a snapshot does not clear a scheme's picks. This
is the same shape as **252**, where `RemoveSnapshot` also leaves the sidecar
artifacts behind while its own doc comment claims otherwise -- worth fixing
together, in one sweep helper, rather than twice.

**`GET /v1/vocabsources` cannot answer "why is this process 1.2 GB".** It
reports `installed.terms` identically whether a scheme serves from its sidecar
or from resident maps. A `sidecar: bool` and a `residentTerms: int` next to it
cost nothing to compute -- `snapshot.sidecar[scheme] != nil` and
`len(snapshot.schemes[scheme])` -- and turn an unexplained memory profile into a
one-line answer.

## Why it still matters after 265

A scheme can *still* fall back to resident maps: its sidecar can go stale (the
source ETag moved), fail to open, or share a source file with a scheme that
could not arm. v0.103.0 made all three log at `WARN` with the term count, but a
log line is not an API. An operator who sees the warning has no way to ask the
running process which schemes are affected, and no way to clear a cache entry
that is holding a scheme dirty.

## Expected

- `DELETE /v1/vocabcache?scheme=&id=` -- librarian-gated, audited, reloads the
  index. Removing the last entry for a scheme should drop it from
  `cachedSchemes`.
- One sweep helper used by `RemoveSnapshot` for both `cache/<scheme>/` and the
  sidecar artifacts (**252**), so "uninstall" means uninstall.
- `sidecar` and `residentTerms` on each entry of `GET /v1/vocabsources`.

## Verify

`~/libcat-e2e/harness/probe_vocab_cache.mjs` and `probe_vocab_sidecar_bypass.mjs`
cover the current behavior; `M9` already asserts the sidecar files survive a
cache write, which a `RemoveSnapshot` sweep must not break.
