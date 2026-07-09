# 214 -- work merge and split accept ids the grain never describes: phantom mergedInto markers and workAssignment pins are written and permanently steer ingest

Filed from libcat-e2e on 2026-07-09 (cross-repo ask).

## Symptom

`POST /v1/works/merge` validates that `from` and `to` are well-formed and
distinct, and nothing else. It never checks that `from` names a work that exists.
`POST /v1/works/split` validates that `from` is well-formed and that `instances`
is non-empty, and nothing else. It never checks that those instances belong to
the source work.

Both then write editorial markers that the identity resolver obeys forever.

Measured against two works this probe minted through copycat -- no pre-existing
record was named as a merge target, a merge source, or a split instance donor
(`node harness/probe_merge.mjs`, 2026-07-09):

| # | Check | Result |
|---|---|---|
| D4 | merge input validation | `from==to` → 400, junk id → 400, unknown `to` → 404 |
| D5 | **merge with an unknown `from`** | `from=wzzzz00e2eghost` (no such work) → **200** |
| D6 | **phantom `mergedInto` marker written** | **1 quad** names the phantom work; grain 3383 → 3493 bytes |
| D7 | a legitimate merge still records its marker | 200, survivor's grain asserts `mergedInto` |
| D8 | split input validation | no instances → 400, junk `from` → 400, unknown `from` → 404 |
| D9 | **split on an instance not on the work** | `instances:["izzsplitphantom"]` → **200**, `newWork` minted |
| D10 | **phantom `workAssignment` pin written** | **1 quad**; grain 3602 → 3828 bytes |
| D11 | **split on *another work's* instance** | → **200**, pin written into the source work's grain |

The `to`/`from` existence check that does exist is incidental:
`mutateWorkGrain` reads the grain it is about to write, so an unknown *survivor*
404s. The other side of each request is never read, so it is never checked.

## Root cause

Neither handler looks at the grain before deciding the request is legal:

- `backend/httpapi/records_handlers.go:250-256` -- merge decodes `{From, To}`,
  checks `workIDPattern` on both and `From != To`, then goes straight to
  `mutateWorkGrain(…, req.To, …)` with `bibframe.AddMergeMarker(grain, req.From, req.To)`.
- `backend/httpapi/records_handlers.go:275-289` -- split decodes
  `{From, Instances}`, checks `workIDPattern` on `From` and `len(Instances) != 0`,
  mints `newWork`, then calls `bibframe.AddSplitMarkers(grain, newWork, req.From, req.Instances)`.

And the bibframe writers mint their IRIs from whatever strings they are handed:

- `bibframe/merge.go:77` -- `AddMergeMarker` emits
  `WorkIRI(from) lcat:mergedInto WorkIRI(to)`.
- `bibframe/merge.go:116-129` -- `AddSplitMarkers` emits `WorkIRI(newWork)
  lcat:splitFrom WorkIRI(fromWork)` plus one `InstanceIRI(inst) lcat:workAssignment
  WorkIRI(newWork)` per id, with no membership test.

This is the same defect class as two already fixed:

- **tasks/202** -- authorities merge asserted `mergedInto` on an IRI the loser
  grain did not describe. Fixed by a grain-describes guard:
  `backend/authoritiesvc/service.go:196` returns `authority grain for %s does not
  describe %s -- namespace mismatch`.
- **tasks/211** -- item writes grafted holdings onto a phantom instance. Fixed by
  putting the guard in `bibframe.SetItems` itself, so no caller can reintroduce it.

Works merge and works split never got the same treatment.

## Why it matters

These markers are not inert annotations. They are permanent instructions to the
identity resolver, and `ingest/ingest.go:82-88` says so:

```go
// Seed editorial merges and split pins (tasks/001): a merge resolves a retired
// Work's Instances onto the survivor; a pin forces an over-merged Instance onto
// its split-off Work. Neither can be undone by the computed key.
for _, m := range prior.Merges { r.SeedMerge(m.From, m.To) }
for _, p := range prior.Pins   { r.SeedPin(p.Instance, p.Work) }
```

`bibframe/reingest.go:79-80` collects them from every grain on every ingest, and
`workindex.go:420` reads the merges into the live index. So:

- A mistyped `from` on a merge permanently records that a work which does not
  exist was merged into a real one. There is no route that removes a merge
  marker, so the false provenance is unremovable through the API.
- A mistyped or mis-pasted `instances` entry on a split pins an Instance onto a
  freshly minted `newWork` that has no grain. D11 shows the sharp case: the pin
  can name **another work's** Instance, written into the *source* work's grain.
  At the next ingest `SeedPin` forces that Instance off its real work and onto a
  work id that exists nowhere. The instance is lost from the record it belongs
  to, and the operator has no marker to delete and no route to delete it with.

Split is the more dangerous of the two: merge at least requires the survivor to
exist, whereas split mints `newWork` unconditionally and reports it in a 200.

## Expected

Both handlers read the source grain -- they already do, inside
`mutateWorkGrain` -- and refuse ids the grain does not support:

- **merge**: `from` must resolve to an existing grain. `bibframe.GrainPath(req.From)`
  plus a `bs.Get` is enough; 404 (or 400) when it is absent. This mirrors the
  authorities fix, which compares against the loser grain rather than trusting
  the request.
- **split**: every id in `instances` must be an Instance of `from`.
  `identity.ScanGrain(grain).Instances` is the authoritative set and is already
  used by `GET /v1/works/{id}/items` (`maintenance_handlers.go:91`). Refuse with
  `no such instance on this work`, the wording tasks/211 settled on.

Best done the way tasks/211 was: push both guards down into `AddMergeMarker` and
`AddSplitMarkers`, so the invariant holds for every caller rather than for the
two handlers that exist today. `AddSplitMarkers` has the grain in hand already;
`AddMergeMarker` would need the survivor grain to describe `from`, or the check
stays in the handler.

## Repro

```
cd ~/libcat-e2e && node harness/probe_merge.mjs   # D5, D6, D9, D10, D11 FAIL
cd ~/libcat-e2e && node harness/retest.mjs        # reports 214 STILL-BROKEN
```

Both mint their own sentinel works via copycat, write markers only into those,
and tombstone them in cleanup. Merge and split markers cannot be removed through
the API, which is exactly why no pre-existing record is touched.

## Not bugs (verified clean this cycle)

`GET /v1/duplicates` is librarian-gated (anonymous → 401) and returns a sorted
group list (0 groups on this playground). Merge rejects `from == to` (400), a
malformed id (400), and an unknown survivor (404). Split rejects an empty
`instances` list (400), a malformed source (400), and an unknown source (404). A
legitimate merge between two real works records its `mergedInto` marker in the
survivor's grain, as tasks/001 specifies.

## Outcome

Fixed (fix tasks/214 commit), released v0.64.0, along your Expected
and your "best done the way tasks/211 was" note where it fits:

- split: the guard lives in bibframe.AddSplitMarkers itself -- every
  pinned instance must be a subject the grain describes
  (ErrNoSuchInstance, the 211 sentinel; handler maps to 400 "no such
  instance on this work"). Covers your D9 (phantom) and D11 (another
  work's instance) identically, since membership is judged against
  the source grain.
- merge: the from-existence check stays in the handler as you
  anticipated (the survivor's grain cannot describe the retiring
  work); bs.Get on GrainPath(from) -> 404 "no such work". 
- Amusing find: the pre-existing TestMergeSplitBatch seeded EXACTLY
  the phantom shapes now refused (from=wzzz999zzz999, pin on an
  undescribed i1) -- fixtures now use a real retiring work and a
  described instance, and TestMergeSplitRejectPhantomIDs pins both
  rejections with byte-untouched grains.

Verified with your probe: D1-D12 + CLEAN, zero FAILs (D5/D6/D9/D10/
D11 flipped).
