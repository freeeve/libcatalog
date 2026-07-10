# 300 -- an approved tag promotion whose rewrite fails is stuck APPROVED forever: cannot retry, cannot delete, cannot re-propose, and the catalog is left half-promoted

Filed from libcat-e2e on 2026-07-10 (cross-repo ask).

`promotion_handlers.go:74-100` decides first and executes second:

```go
promo, err := svc.DecidePromotion(r.Context(), req.Tag, req.Approve, id.Email)  // :74  status := APPROVED, persisted
…
works, err := promoter.PromoteTag(r.Context(), promo, id.Email)                 // :82  rewrites every tagged work
if err != nil {
	…
	writeError(w, http.StatusInternalServerError, "rewrite failed")            // :98  the APPROVED stamp stays
	return
}
_ = svc.MarkPromotionExecuted(r.Context(), promo.Tag, works)                    // :100 never reached
```

The stamp is durable before the work is done. When `PromoteTag` fails, nothing rolls it back,
and the record is then unreachable in **every** direction:

| escape | why it fails |
|---|---|
| retry the approval | `DecidePromotion` rejects anything not `PENDING` (`promotion.go:120-125`) |
| delete the record | there is no `DELETE` route -- `promotion_handlers.go` mounts `GET /v1/promotions`, `POST /v1/promotions`, `POST /v1/promotions/decide`, and nothing else |
| re-propose the tag | `ProposePromotion` supersedes only a `REJECTED` record (`promotion.go:62-76`); `PENDING` and `APPROVED` get `ErrPromotionExists` |

`promotionKey(tag)` is `PROMOS` / `<tag>` (`promotion.go:34-36`) -- one record per tag,
written under `CondIfAbsent`. So the tag stays on every work, unpromoted, behind a promotion
the queue calls approved.

**The handler already knows this state exists.** With no publisher wired it answers
`"publisher not configured; approved but not executed"` (`:103`). That is the same stuck
record, reached on purpose.

## Symptom

Measured on a throwaway writable clone of the playground (`:8468`), with one grain shard
chmod'd read-only to make the rewrite fail. Controls first: on a healthy store the whole flow
works -- propose, approve, `200`, `works=1`, and the editorial folk tag is retracted from the
grain.

Then, with the shard read-only:

```
POST /v1/promotions/decide {tag, approve:true}   -> 500 {"error":"rewrite failed"}

GET  /v1/promotions   ->  status=APPROVED, works=0
GET  /v1/works/<id>   ->  <#…Work> <…ns#tag> "zz-e2e-promo-bad-65vgh" <editorial:> .   still there
```

Restore the permissions -- the store is healthy again -- and there is still no way out:

```
POST /v1/promotions/decide  (retry)   -> 409 suggest: promotion for "…" already APPROVED
DELETE /v1/promotions/<tag>           -> 404   (no such route)
POST /v1/promotions        (re-propose)-> 409 promotion already proposed
```

End state: `status=APPROVED`, `works=0`, and the work still carries the folk tag.

### The rewrite is partial, and the count of what landed is thrown away

`PromoteTag` (`promote.go:47-70`) walks the tagged works and returns on the **first** store
failure:

```go
for _, summary := range summaries {
	…
	etag, err := MutateGrain(ctx, p.Blob, path, func(old []byte) ([]byte, error) { … })
	if err != nil {
		return rewritten, fmt.Errorf("promote %q on %s: %w", promo.Tag, summary.WorkID, err)
	}
```

Works ahead of the failing one are already rewritten -- subject added, folk tag retracted. The
handler discards that count on the error path and never calls `MarkPromotionExecuted`.

Measured, with two works in different grain shards and one shard read-only:

```
approve -> 500
  w0cfnsjg6micju  tag retracted = true    (rewritten)
  w1dh6vtir43o8i  tag retracted = false   (untouched)
the promotion records works = 0
```

**One of two works promoted; the record says zero.** `PromoteTag` returned `1`; the handler
dropped it.

### What the librarian sees

`Promotions.svelte` renders the stuck record in its **Decided** section:

```svelte
<span class="chip chip--{p.status === 'APPROVED' ? 'ok' : 'no'}">{p.status}</span>      :202
{#if p.status === "APPROVED"}<span>{p.works ?? 0} work{…} rewritten</span>{/if}          :207
```

A green `APPROVED` chip and **"0 works rewritten"**, permanently. The `Approve`/`Reject`
buttons live only in the Pending section (`:179-180`), so a decided row has no control of any
kind -- the same shape as tasks/292: an affordance-free row that produces no error to notice.

## Root cause

`backend/httpapi/promotion_handlers.go:74-100`. The status transition is committed to the
store before the operation it describes has run, and the failure path has no compensation.

`DecidePromotion` (`suggest/promotion.go:120-133`) is a pure state machine: `PENDING ->
APPROVED | REJECTED`, one way, guarded on `p.Status != StatusPending`. It is correct in
isolation. It just gets called too early, and there is no `PENDING`-restoring counterpart, no
`EXECUTING` state, and no `DELETE`.

This is the same family as tasks/261 (attachments wrote the grain before the bytes) and
tasks/115 (write the new partition before deleting the old): **the durable record of an
intention is written before the intention is carried out, and nothing reconciles them.**

## Why it matters

**A tag promotion is the single widest write in the product.** `PromoteTag` rewrites every
work carrying the tag -- the handler's own comment says *"this is the request that touches the
most records at once"* (`:83-85`). It is exactly the request most likely to hit a transient
store failure partway, and the one whose partial application is hardest to spot.

**The catalog is left inconsistent with no record of how far it got.** Some works have the
authority subject and no folk tag; the rest have the folk tag and no subject. Nothing in the
product distinguishes them. `works=0` actively misleads: it says nothing happened.

**Recovery requires editing the store by hand.** There is no route, no screen, and no role
that can clear a `PROMOS` record. A library that hits this once has a tag it can never promote
and a promotions queue with a permanent lie in it.

**It has been costing this harness a check for months.** `harness/retest.mjs`'s `t203` has
been `SKIPPED` on every cycle since it was written, with the reason *"mutating: approving
leaves an undeletable promotion record"*. A regression check that never runs is a check that
stopped measuring. It skips because of this bug.

## Expected

- **Execute, then stamp.** Run `PromoteTag` first and only write `APPROVED` when it returns
  cleanly. A failure then leaves the promotion `PENDING`, the queue honest, and the Approve
  button live -- retry is free and needs no new route. The audit entry (`PROMOTION_APPROVE`,
  `promotion.go:139-144`) should move with it.

  The objection is that `PromoteTag` takes `promo` and so wants a decided record. It does not:
  `grep -n 'promo\.' backend/publish/promote.go` reads only `promo.Term` and `promo.Tag`. The
  status is never consulted. Passing the pending promotion is enough.

- **Make the rewrite resumable or atomic.** If executing first is not acceptable, add an
  `EXECUTING` state and a route that re-drives it, and have `MarkPromotionExecuted` record the
  partial count on the failure path too -- `PromoteTag` already returns it. A promotion that
  says `1 of 12 works rewritten` is recoverable; one that says `0` is not.

  Re-driving is already safe, and not because the write is idempotent: the loop **skips works
  that no longer carry the tag** --

  ```go
  for _, summary := range summaries {
  	if !slices.Contains(summary.Tags, promo.Tag) { continue }   // promote.go:47-48
  ```

  A work rewritten on the first attempt had its folk tag retracted, so the second attempt
  passes over it and resumes at the one that failed.

- **Add `DELETE /v1/promotions/{tag}`**, librarian-gated, for the states the state machine
  cannot leave. Even with the fix above, the `"publisher not configured; approved but not
  executed"` branch (`:103`) mints exactly this record on purpose.

- **Do not discard `MarkPromotionExecuted`'s error** (`:100`, `_ =`). A successful rewrite
  whose stamp fails reports `works=0` and looks identical to this bug.

## Repro

```bash
cd ~/libcat-e2e && node harness/probe_promotion_stuck.mjs   # P3-P8
cd ~/libcat-e2e && node harness/retest.mjs                  # check t300
```

**Touches neither `:8481` nor `:8501`.** It boots its own writable clone of the playground on
`:8468` (`cp -Rc`, APFS copy-on-write), chmods one grain shard read-only to induce the store
failure, restores the mode, and deletes the clone wholesale. An undeletable promotion record
costs nothing there -- which is the point, and the reason `t203` could never run on the shared
playground.

Its controls carry the argument. **`P1` runs the entire flow on a healthy store: propose,
approve, `200`, `works=1`, folk tag retracted from the grain.** So the stuck record below is
the induced fault and not a broken feature. `P2` shows the chmod really does make the write
fail (`500`), so `P3` is about a failed rewrite rather than a rewrite that never started.
`P8` refuses to conclude when the read-only shard happens to be walked first -- 0 of 2
rewritten would make `works=0` correct, and the check would pass for the wrong reason.

An earlier run of this probe reported "the folk tag was retracted" for *every* case, including
the failed ones. `GET /v1/works/{id}/doc` maps the grain into profile fields and has no raw
quads, so the regex matched nothing and answered "no tag" always. `P0` -- which asserts the
sentinel tag **is** on the work right after it is added -- caught it. The probe now reads
`GET /v1/works/{id}`'s `nquads`.

## Outcome

Fixed in **v0.123.0**, commit `b15b448`. Every bullet under **Expected** shipped.
The report's diagnosis was right, including the observation that unblocks the
whole fix: `PromoteTag` never consults the status.

### Execute, then stamp

`grep -n 'promo\.' backend/publish/promote.go` reads only `promo.Term` and
`promo.Tag`, exactly as the report says. So the pending record is all the rewrite
needs, and the durable stamp no longer precedes the work it describes.

Approval is now **one CAS write** that flips PENDING -> APPROVED *and* records the
count. Splitting them left a window where a promotion read as APPROVED with
`works: 0` -- the same lie, briefly, and permanently if the second write failed.

A failure leaves the promotion PENDING with its partial count recorded. The
Approve button stays live, and retry is free. `DecidePromotion` is gone, replaced
by `ApprovePromotion(tag, actor, works)` and `RejectPromotion(tag, actor)`.

### The count

`RecordPromotionWorks` stamps the partial count on the failure path without
touching the status, and `ApprovePromotion` adds to it, so the counts accumulate
across attempts. The `_ =` on the stamp is gone: a rewrite whose record fails now
answers `500 promotion applied but not recorded; approve again to record it`
rather than printing `works=0` over work that happened.

### DELETE

`DELETE /v1/promotions/{tag}`, librarian-gated, with an audit entry. It frees the
tag to be proposed again. The `"publisher not configured; approved but not
executed"` branch mints exactly the record it exists for.

### The SPA

Decided rows had no control of any kind (the tasks/292 shape). They carry a
**Delete** now. A pending row whose earlier approval failed partway shows
`N works already rewritten by a failed attempt`, and its button reads **Resume**
rather than Approve.

### What the report got right that I checked, and one thing it got half-right

The claim that retrying is safe *without idempotence* -- because the loop skips
works that no longer carry the tag -- is true, and it is now pinned by a test
against the real publisher with a store that fails on the second grain write:
`TestPromoteTagResumesAtTheWorkThatFailed` retries and rewrites exactly the one
that failed.

But it only holds for **editorial folk tags**. `seedTaggedWork` in the existing
suite plants a *feed-side* tag as well, and `PromoteTag` deliberately never
retracts those (the projector's alias suppression hides them). Such a work matches
the loop on every pass, so a retry rewrites it again -- safely, since the write is
idempotent, but the accumulated count can then exceed the number of distinct works
touched. My first version of the resume test asserted 1 and got 2, which is how I
found it.

That is now three tests, not one: the partial count, the resume-and-skip, and
`TestRetryRewritesFeedTaggedWorksAgain`, whose failure message says which doc
comment to update if `PromoteTag` ever starts retracting feed tags. `Promotion.Works`
documents that it counts rewrites, not distinct works, and when the two diverge.

### Mutation-tested

- **M1**, stamp APPROVED before the rewrite (the original bug): all four new
  httpapi tests go red, including `PromoteTag was handed a APPROVED promotion` --
  the fake promoter refuses a decided record, so the success case is evidence and
  not coincidence.
- **M2**, discard the partial count (the reported `_ =`):
  `TestPartialRewriteRecordsHowFarItGot` fails on its own with
  `works = 0 after a 1-of-2 rewrite, want 1`.

Gates: `gofmt -s`, `go vet`, root + backend `go test ./...`, 287 SPA unit tests,
and `TestAPIReferenceMatchesRouter` (which caught the undocumented DELETE route
before I did).

`t203` in the harness should un-skip: approving no longer leaves an undeletable
record, and if one is ever left, `DELETE` clears it.
