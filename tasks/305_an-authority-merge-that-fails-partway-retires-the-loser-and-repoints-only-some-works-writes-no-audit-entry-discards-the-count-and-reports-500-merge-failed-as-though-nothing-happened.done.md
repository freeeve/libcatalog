# 305 -- an authority merge that fails partway retires the loser and repoints only some works, writes no audit entry, discards the count, and reports 500 merge failed as though nothing happened

Filed from libcat-e2e on 2026-07-10 (cross-repo ask).

`authoritiesvc/service.go:199-232` writes the retirement first and does the work second:

```go
if _, err := publish.MutateGrain(ctx, s.Blob, loserPath, func(old []byte) ([]byte, error) {
	return bibframe.AddAuthorityMergeMarker(old, loserURI, winner.ID, LocalScheme)   // :200
}); err != nil {
	return MergeResult{}, err
}
…
for _, summary := range summaries {
	if !slices.Contains(summary.Subjects, loserURI) { continue }
	if _, err := publish.MutateGrain(ctx, s.Blob, path, func(old []byte) ([]byte, error) {
		return bibframe.ReplaceSubjectReference(old, workID, loserURI, subject, winner.Scheme)
	}); err != nil {
		return result, fmt.Errorf("rewrite %s: %w", workID, err)                     // :219
	}
	result.Rewritten++                                                               // :221
}
s.audit(ctx, suggest.AuditEntry{Action: "AUTHORITY_MERGE", …})                        // :224-228
…
return result, s.Reload(ctx)                                                          // :232
```

The loser is marked `lcat:mergedInto` -- **retired** -- before a single work is repointed. The
rewrite loop then returns on the **first** failing work, so the works ahead of it are already
repointed and the works behind it still name the retired term. The audit entry (`:224`) and
`s.Reload` (`:232`) are both past that return, so nothing records that any of it happened and
the in-memory vocabulary index never learns the loser was retired.

The handler discards `result` and answers with a flat failure
(`backend/httpapi/authorities_handlers.go:188-195`):

```go
case errors.Is(err, publish.ErrGrainStore):
	// Merge writes through publish.MutateGrain twice, so a store failure
	// used to answer 409 with an *os.PathError as its body: the wrong
	// status, claiming a concurrent edit, and the wrong message, naming
	// the blob root (tasks/272).
	logger.Error("authority merge failed", …)
	writeError(w, http.StatusInternalServerError, "merge failed")
```

**The handler already knows the merge writes twice.** tasks/272 looked at that fact and fixed
the error *message*. Nobody looked at the state the two writes can leave behind.

## Symptom

Measured on a throwaway writable clone of the playground, pinned to committed HEAD, with two
sentinel works in **different grain shards** carrying a sentinel local heading, and one of those
two shards `chmod`'d read-only to make the second rewrite fail.

Controls first. A healthy merge on its own loser works end to end: `200`, `rewritten=2`, neither
work still names the loser, and the loser's grain survives carrying one `mergedInto` quad
(merge retires, it does not delete). The `chmod` really does break it: `500 {"error":"merge
failed"}`.

Then, with the shard read-only:

```
POST /v1/authorities/merge {loser, winner}   -> 500 {"error":"merge failed"}

the loser's grain                  1 mergedInto quad     <- retired anyway
w0cfnsjg6micju                     repointed  = true     <- rewritten
w1dh6vtir43o8i                     repointed  = false    <- still names the retired term
AUTHORITY_MERGE audit entries      1 before, 1 after     <- none written
the 500 body                       {"error":"merge failed"}   <- no count of what landed
```

**One of two works repointed. The loser retired. The audit log says the merge never happened.**
`MergeResult.Rewritten` was `1` when `:219` returned it; the handler dropped it on the floor.

### The server disagrees with its own disk

`s.Reload(ctx)` is on the success path only. After the failed merge the loser's grain carries
the retirement, but the running process still believes the term is live:

```
grain on disk                                 1 mergedInto quad
GET /v1/terms/resolve?id=<loserURI>           mergedInto = (absent)
GET /v1/authorities                           still offers the loser
```

The control that makes this readable: after a **clean** merge, `Reload` runs and the same
endpoint answers `mergedInto=<winner IRI>`. So the field is exposed and the endpoint is not
declining to say -- the server is simply out of date with the store, and stays that way until
something else reloads or the process restarts. Two operators looking at the same catalog, one
on a freshly-restarted process, disagree about whether the heading exists.

(`vocab.Term.MergedInto`'s own comment: *"Retired terms resolve via Lookup (so old references
still label) but leave the search index."* Both `Terms()` and `Search()` in fact still return
retired local headings; only the sidecar's built index drops them. So the Authorities screen
lists the loser either way. **This is not a dangling-subject bug** -- the leftover reference
still labels. It is a silent, half-applied, unaudited state.)

### It is recoverable -- by a retry nobody is told to make

Unlike tasks/300's promotion, `Merge` has no state machine and no guard on an already-retired
loser. With the store healthy again, re-issuing the identical request succeeds:

```
POST /v1/authorities/merge (retry)  -> 200 {"rewritten": 2}
both works repointed; the loser still carries exactly 1 mergedInto quad
```

`AddAuthorityMergeMarker` is idempotent and the rewrite loop skips works that no longer name the
loser, so the retry resumes at the one that failed. **That is the good news and the whole
problem**: the catalog is one retry away from correct, and nothing tells anyone to retry.
The response says `merge failed`. The audit log shows nothing. The screen shows the heading
still there. A cataloger's only reasonable reading of `500 merge failed` is "nothing happened,
the heading is intact, try again later" -- and if they never do, the half-merge is permanent and
invisible.

## Root cause

`backend/authoritiesvc/service.go:199-201`. The durable record of the retirement is written
before the work it describes is done, and the failure path has no compensation. Same family as
**tasks/300** (promotion stamped `APPROVED` before `PromoteTag` ran), **tasks/261** (the
attachment grain written before the bytes) and **tasks/115**.

Three separate consequences share that one cause:

1. `:200` retires the loser before any work is rewritten.
2. `:219` returns on the first failure, abandoning the loop mid-corpus.
3. `:224` and `:232` -- the audit entry and the index reload -- sit past that return, so the
   partial state is neither recorded nor reflected.

And `authorities_handlers.go:188-194` discards the `MergeResult` the error path took the trouble
to return: `result` is only ever written out by the `writeJSON(w, http.StatusOK, result)` on the
success path at `:200`.

## Why it matters

**A merge is a global heading update.** The doc comment says so (`:165-170`): *"every Work grain
referencing the loser is rewritten to the winner in one batch pass … references live in the
editorial graph, so the rewrite is a global heading update."* It is the widest authority write in
the product and the one most likely to meet a store failure partway -- the same argument
tasks/300 makes about `PromoteTag`, one package over.

**The catalog is left describing the same concept two ways.** Some works carry the winner's IRI,
the rest carry a retired local IRI. Both still label, so nothing looks broken. Faceting splits
the concept in two: a patron browsing the winner heading sees a subset of the works about it, and
no count anywhere is wrong enough to notice.

**`500 merge failed` is not true.** It is the one message guaranteed to stop anyone
investigating. The failure it names is real; the implication -- that the merge did not happen --
is false. This is worse than tasks/300's stuck-`APPROVED` record, which at least *showed* a
wrong state on the screen. Here there is nothing to see.

**The audit trail cannot help.** `AUTHORITY_MERGE` is written only on success, so the one record
of "a heading was retired and N works were repointed" does not exist for exactly the runs where
somebody would need it. tasks/299 established that half the audit trail has no reader; this is
half of a merge with no *writer*.

**A stale index makes it non-deterministic.** Whether the loser looks retired depends on when
`lcatd` last restarted, not on what the store says.

## Expected

- **Rewrite the works, then retire the loser.** Nothing in the rewrite loop needs the marker:
  it matches on `loserURI` and writes `subject`, neither of which comes from the loser grain's
  retirement statement. Marking last means a failure leaves the heading live, every work either
  repointed or not, and a retry that is simply the same merge again. This is the same
  execute-then-stamp shape that fixed tasks/300.

- **Record the partial count and audit it.** `:219` already returns `result` with `Rewritten`
  set. Write an `AUTHORITY_MERGE` entry on the failure path too, carrying the count and the fact
  that it did not finish, and have the handler put the count in the response body. A merge that
  says `rewrote 1 of 2 works, then failed` is recoverable; `merge failed` is not.

- **Reload on the way out, not on the way through.** `defer s.Reload(ctx)` (or reload before
  every return) so the in-memory index never contradicts the grain it was built from.

- **Say what to do.** If the operation is resumable -- and it is, verified -- the 500 should say
  so: `merge partially applied: 1 of 2 works rewritten; retry to finish`.

- **Unrelated, spotted while reading:** `authorities_handlers.go:196-197` is a fallthrough
  `case err != nil: writeError(w, http.StatusConflict, err.Error())`. Any error that does not
  match one of the classified cases above it -- a read failure on the loser grain, an
  `ingest.SummariesOf` failure -- still reaches the client as a `409` carrying the raw error
  string. That is precisely the tasks/272 shape, surviving on the one path 272 did not
  enumerate.

## Repro

```bash
cd ~/libcat-e2e && node harness/probe_authority_merge_partial.mjs   # M3, M4, M5, M7
cd ~/libcat-e2e && node harness/retest.mjs                          # check t305
```

**Touches neither `:8481` nor `:8501`.** It boots its own writable clone of the playground
(`cp -Rc`, APFS copy-on-write) pinned to committed HEAD, mints sentinel `zz-e2e-*` headings,
attaches one to two works in distinct grain shards, `chmod`s one shard read-only, restores the
mode, and deletes the clone wholesale. The tracker had recorded this call site as unreachable --
*"inducing a failure on an authority grain needs a merge rig this harness does not have."* It
needs the rig `t272` and `t300` already use.

Its controls carry the argument. **`M1` runs a whole healthy merge first** (`200`,
`rewritten=2`, both works repointed), so the broken state below is an induced failure and not a
merge that never worked. `M2` shows the `chmod` really does make the second write fail, so `M3`
is about a failed rewrite rather than one that never started. `M6` shows merge retires rather
than deletes, so the leftover reference names a grain that exists. **`M9` is the one that makes
`M7` legible**: after a clean merge `resolve` *does* report `mergedInto`, so its absence after a
failed merge is the server disagreeing with its disk, not the endpoint declining to answer.

Two harness errors were caught before they became claims in this report. `vocab.Term.ID` **is**
the authority URI and there is no `uri` field, so matching the list against the short minted id
never matched, and the probe first reported *"the authority list no longer offers the loser"* --
for every heading that has ever existed. And `Terms()` does not filter `mergedInto` at all, so
the "dangling subject" this report nearly asserted does not exist: the leftover reference still
resolves and still labels. Both were found by reading the Go rather than trusting a green-looking
`FAIL`.

## Outcome

Fixed in **v0.126.0**, commit `2dbf5fb`. Every bullet under **Expected** shipped,
including the unrelated `409` fallthrough.

### Execute, then stamp

The rewrite loop runs first; the loser is retired only when it returns cleanly.
Your reading was right and it is what unblocks the fix: nothing in the loop needs
the marker. `winnerSubject` reads the **vocab index** for the winner, and
`ReplaceSubjectReference` takes `loserURI` plus that subject. I checked both before
reordering.

A failure now leaves the heading live and every work either repointed or not.
Re-issuing the same request resumes -- `AddAuthorityMergeMarker` is idempotent and
the loop skips works that no longer name the loser -- and that is now pinned by a
test against the real service with a store that fails on the second work grain.

### The count, the audit, the notify

`MergeResult` gained `Carriers` (works naming the loser when the pass began) and
`Complete`. `Rewritten < Carriers` is the signal the merge did not finish.

`AUTHORITY_MERGE` is written on **both** paths, carrying
`partial: rewrote 1 of 2 works, then failed; heading not retired, retry to finish`.
The trigger notify fires on both paths too, with exactly the grains that were
rewritten -- you did not ask for this, but it belongs to the same return: a
half-rewritten corpus never triggered a reprojection, so the built site kept the
old subjects until something unrelated moved it.

The handler no longer discards `MergeResult`. It distinguishes three outcomes,
which is one more than expected:

| response | meaning |
|---|---|
| `500 merge partially applied: i of n works rewritten; the heading is still live, retry to finish` | resumable |
| `500 merge failed; nothing was changed` | the first write failed |
| `500 merge applied, but the vocabulary index was not reloaded` | the store is correct; nothing to redo |

The third exists because folding `Reload`'s error into the error branch would
otherwise report a **finished** merge as partial. `Complete` separates them.

### `409` fallthrough

Gone. Unclassified errors -- a read failure on the loser grain, a `SummariesOf`
failure -- answered `409` with the raw error string. That is the tasks/272 shape on
the one path 272 did not enumerate, exactly as you said. They are server faults and
answer `500`.

### Mutation-tested, and one thing you asked for that the tests cannot see

- retire before rewriting (the original bug): `TestFailedMergeLeavesTheHeadingLive`
  fails.
- audit on the success path only: `TestFailedMergeIsAudited` fails.
- notify on the success path only: `TestFailedMergeNotifiesTheGrainsItRewrote` fails.
- handler answers a flat `merge failed`: two tests fail, including the tasks/272 one.

- **reload on the success path only: nothing fails.** Your third bullet -- "reload on
  the way out, not on the way through" -- is *subsumed by the reorder*. With the
  marker written last, a failed merge changes no authority grain, so there is
  nothing for the index to be stale about. The index/disk disagreement you measured
  was a symptom of the write order, not of where `Reload` sat. Merge still reloads on
  both paths as defence in depth, and `TestIndexAgreesWithTheStoreAfterEitherOutcome`
  pins the invariant an operator can see -- but its comment says plainly that it
  cannot observe the reload. A test that passes under its own mutation is not
  evidence, and it should not be presented as any.

Gates: `gofmt -s`, `go vet`, root + backend `go test ./...`, 288 SPA unit tests,
`svelte-check`, and `TestAPIReferenceMatchesRouter`.

### For your harness

`t305` should go green. `probe_authority_merge_partial.mjs`'s `M3`/`M4` will now see
the heading **live** after the failed merge, `M5` an `AUTHORITY_MERGE` entry whose
note begins `partial:`, and the 500 body carrying `rewritten`, `carriers` and
`complete`.

**`TestAuthorityMergeStoreFailureIsNotAConflict` changed its expected message** from
`merge failed` to `merge failed; nothing was changed`. Anything of yours that pins
the old literal needs updating. `docs/api.md` documents every code under
"`POST /v1/authorities/merge`: the heading is retired last".
