# 257 -- POST /v1/review reports reviewed=len(decisions), counting decisions it silently skipped, so a stale moderator decision is discarded but shown as applied

Filed from libcat-e2e on 2026-07-09 (cross-repo ask).

Found while exercising the review queue's apply path (`PublishBar`, the last
never-covered UI surface). `Review` correctly refuses to re-decide a suggestion that
someone else already resolved -- and then the handler tells the caller it did.

## Symptom

`POST /v1/review` answers `{"reviewed": N}` where **N is the number of decisions
submitted**, not the number applied. Decisions that `Review` skipped are counted.

Measured on :8481 against a `folk`-scheme sentinel suggestion (never published, so
the grain store is untouched):

```
mint suggestion                          -> status=PENDING

A rejects it
  POST /v1/review [1 decision]           -> 200 {"reviewed":1}
  suggestion: REJECTED, reviewNote="zz-e2e A rejects"        <- control: a real transition

B approves the same suggestion (their queue page predates A's apply)
  POST /v1/review [1 decision]           -> 200 {"reviewed":1}      <- the lie
  suggestion: still REJECTED, still reviewNote="zz-e2e A rejects"
  approved queue: sentinel absent                            <- B's approve did nothing

a decision naming a suggestion that never existed
  POST /v1/review [1 decision]           -> 200 {"reviewed":1}      <- also counted

a mixed batch: 1 stale decision + 1 live one
  POST /v1/review [2 decisions]          -> 200 {"reviewed":2}      <- 1 was applied
```

And through the real UI, the same race end to end. B opens `#/queue`, clicks
**Reject** on the sentinel row (`PublishBar` reads *"1 staged · 0 approve · 1
reject"*), A resolves it out of band, then B clicks **Apply**:

```
screen says            : "reviewed 1"
stored decision        : reviewNote="zz-e2e A (out of band)", reviewedBy=A
```

B is told their decision was reviewed. It was thrown away.

The audit trail, by contrast, is honest -- a skipped decision writes no entry:

```
audit for the sentinel : 2 REVIEW_REJECT, 0 REVIEW_APPROVE   (B's approve leaves none)
```

So the state is right, the audit is right, and only the number handed back to the
human is wrong.

## Root cause

`backend/suggest/review.go:209-221` -- `transition` refuses a suggestion that is no
longer decidable, and `Review` swallows that as a skip:

```go
key := store.Key{PK: workPK(d.WorkID), SK: suggSK(d.Term, d.Type)}
err := s.transition(ctx, key, to, func(sg *Suggestion) { … })
if errors.Is(err, errAlreadyResolved) || errors.Is(err, store.ErrNotFound) {
	continue
}
```

The guard it trips is `backend/suggest/service.go:338-339`:

```go
if sg.Status != StatusPending && sg.Status != StatusDisputed {
	return nil, errAlreadyResolved
}
```

Both branches are correct: this is exactly the optimistic-concurrency check the queue
needs, and the `continue` also skips `writeAudit` (`review.go:230`), which is why the
trail stays clean. But `Review` returns only `error` -- it never reports *how many* of
the decisions it actually applied. So `backend/httpapi/review_handlers.go:113` has
nothing to count but the request:

```go
resp := map[string]any{"reviewed": len(req.Decisions)}
```

and `backend/ui/src/screens/Queue.svelte:167` faithfully echoes it:

```ts
const parts = [`reviewed ${res.reviewed}`];
…
notice = parts.join(" · ");
decisions.clear();
```

`decisions.clear()` then discards B's staged decisions, so nothing on screen survives
to contradict the notice.

## Why it matters

Two moderators working the same queue is the ordinary case, not an exotic one -- the
queue is a shared worklist, `GET /v1/queue` has a cursor and a `Load more`, and
`PublishBar` exists precisely to apply a *batch* of decisions built up over minutes of
reading. Any decision staged before someone else's apply is stale.

The failure is silent and directional. B's **approve** of a term A **rejected** is
dropped, so:

- the term stays rejected and never publishes, while B believes they approved it;
- the reverse -- B's reject of a term A approved -- leaves the term APPROVED and in
  the publish worklist (`review.go:365`, `Status == APPROVED && PublishedETag == ""`),
  so it goes into the grain on the next **Apply & publish**, over B's objection, with
  B having been told their rejection was reviewed.

This is the lost-update problem the record editor already solves: records carry an
ETag and answer `412` on a stale write, and the editor shows *"This record changed
while you were editing"* and keeps the staged edit (tasks/195 and the concurrency
checks C1-C8). The review queue has the same hazard, detects it correctly, and then
throws the detection away instead of reporting it.

Nothing is corrupted. What is lost is a moderator's decision, plus any chance of
noticing.

Note the response already has the vocabulary for this: the publish half of the very
same JSON reports `skipped` (`backend/publish/publisher.go:72,150`), and
`Queue.svelte:169` renders it. Review reports no such thing.

## Expected

- **`Review` should return what it did**, e.g. `(applied int, skipped []Decision,
  err error)`, or an outcome per decision. The information exists at `review.go:219`;
  it is discarded one line later.
- **The handler should report both**, mirroring publish:
  `{"reviewed": applied, "skipped": len(skipped)}` -- so `reviewed` means *reviewed*.
- **The queue screen should tell the moderator, and not silently clear their work.**
  `Queue.svelte:172` calls `decisions.clear()` unconditionally. When a decision is
  skipped, the honest behaviour is the editor's: say *"3 applied, 1 was already decided
  by someone else"*, keep or re-stage the skipped ones against their new status, and
  let the moderator look. Reloading the queue (`load(true)`) already fetches the truth;
  only the notice and the clear need to learn about it.

If counting submissions is deliberate, then the field is misnamed and the UI's
*"reviewed N"* should not be phrased as an accomplishment.

## Repro

```bash
cd ~/libcat-e2e && node harness/probe_review_apply.mjs   # R2, R4, R5, R7, R8
cd ~/libcat-e2e && node harness/retest.mjs               # check t257
```

(`probe_review_apply.mjs` mints suggestions, so repeated runs inside one hour hit the
per-supporter rate cap, `suggest/service.go:48-60`, and report `R0 ERR: rate limited`.
`t257` instead decides suggestions that never existed -- `transition` returns
`store.ErrNotFound` before any write -- so it mutates nothing and costs no budget.)

By hand, against :8481. A `folk` term needs no controlled vocabulary, and a challenge
token must be >= 3s old (`suggest/abuse.go:25`):

```bash
TOK=…
W=$(curl -s -H "Authorization: Bearer $TOK" 'localhost:8481/v1/works?limit=1' | jq -r '.works[0].workId')
C=$(curl -s localhost:8481/v1/challenge | jq -r .challenge); sleep 4

curl -s -XPOST -H 'Content-Type: application/json' localhost:8481/v1/suggestions \
  -d "{\"workId\":\"$W\",\"term\":{\"scheme\":\"folk\",\"id\":\"zztest\"},\"type\":\"ADD\",\"reason\":\"MISSING\",\"challenge\":\"$C\"}"
# 202

review() {  # $1 = approve (true|false), $2 = note
  jq -nc --arg w "$W" --argjson a "$1" --arg n "$2" \
    '{decisions:[{workId:$w,term:{scheme:"folk",id:"zztest"},type:"ADD",approve:$a,note:$n}],publish:false}' \
  | curl -s -XPOST -H "Authorization: Bearer $TOK" -H 'Content-Type: application/json' \
      --data-binary @- localhost:8481/v1/review
}

review false A   # {"reviewed":1}  -- applied; the suggestion is now REJECTED
review true  B   # {"reviewed":1}  -- applied nothing; still REJECTED, note "A"

curl -s -H "Authorization: Bearer $TOK" 'localhost:8481/v1/queue?status=REJECTED' \
  | jq '.items[] | select(.term.id|test("zztest")) | {status, reviewedBy, reviewNote}'
```

(`status` is the uppercase `Status` constant -- `?status=pending` reads an empty index
partition and returns `[]` rather than an error.)

Or in the UI: open `#/queue` in two tabs, click **Reject** on a row in tab one and
**Apply**; then in tab two (whose page predates that) click **Approve** on the same row
and **Apply**. Tab two says *"reviewed 1"*, and the suggestion is still rejected.
