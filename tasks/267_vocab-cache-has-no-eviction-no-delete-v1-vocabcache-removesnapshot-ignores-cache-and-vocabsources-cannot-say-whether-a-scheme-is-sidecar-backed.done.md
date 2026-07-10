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

## Outcome

Shipped in **libcat v0.146.0** (minor -- a new route, a new audit action, and two
additive fields). All three Expected items landed.

**1. Eviction.** `DELETE /v1/vocabcache?scheme=&id=` -- librarian-gated (like the
POST) and audited (`VOCAB_CACHE_REMOVE`, note `scheme: id`, reusing the audit
queue wired in tasks/259). `RemoveCachedTerm` deletes the one blob and reloads;
an absent pick is a `404`, not a silent success. Dropping a scheme's last pick
removes it from `cachedSchemes`, so the reload also drops it from the filter
unless a snapshot or the base filter still holds it.

**2. One sweep.** `RemoveSnapshot` now calls a `sweepScheme` helper that removes
`cache/<scheme>/` **and** the sidecar artifacts (the latter it already did,
tasks/252). Uninstalling a snapshot no longer leaves a scheme's live picks
behind holding it dirty. The M9 guarantee holds: the sweep is only in
`RemoveSnapshot`, so a cache *write* still leaves the sidecar untouched.

**3. Serving visibility.** `GET /v1/vocabsources` carries `sidecar bool` and
`residentTerms int` per row, from a new nil-safe `Index.SchemeStats(scheme)`
(`snapshot.sidecar[scheme] != nil`, `len(snapshot.schemes[scheme])`). The SPA's
vocab screen shows a `sidecar` badge (with the resident overlay count in its
tooltip) or a `· N resident` note. `RemoveSnapshot` returns the removed
`InstallInfo` so the count is read once.

### Verified

- `TestVocabCacheEvictionAndSweep` (service): two picks cached, one evicted, the
  other survives, re-evict is `NotFound`; a cache write leaves the sidecar count
  unchanged; `Views` fields equal `SchemeStats`; `RemoveSnapshot` leaves zero
  cache blobs and zero sidecar artifacts. `TestVocabCacheRemoveIsAudited`
  (handler): the DELETE is librarian-gated, audited, and 404s an absent pick.
  Backend `go test ./...` green; UI 324/324; `docs/api.md` regenerated.
- Live on `:8481`: `GET /v1/vocabsources` shows **`lcsh: installed=true
  sidecar=true residentTerms=0 terms=513125`** -- the exact "why is this process
  1.2 GB" answer the task asked for. A cached pick evicts (200) and lands a
  `VOCAB_CACHE_REMOVE` entry naming the scheme; re-evicting 404s.
