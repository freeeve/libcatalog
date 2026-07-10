# 328 -- the Dashboard "Pending review" tile counts a capped page, so a backlog over 50 shows as 50

Filed from libcat-e2e on 2026-07-10 (cross-repo ask).

A look-and-feel / correctness bug on the landing screen. The moderator's
call-to-action tile silently understates the review backlog once it passes 50 --
the same "a count read off a capped page" shape as the OPAC facet bug (281).

## Symptom

The Dashboard's "Pending review" line is computed from a single call to
`GET /v1/queue` and displayed as a bare number:

```
loadPending(): pending = page.items.length          // Dashboard.svelte:99-100
template:      {pending} pending suggestion{s}       // Dashboard.svelte:232-233
```

`GET /v1/queue` is cursor-paginated with a default page size of 50, so
`page.items.length` never exceeds 50 however large the real backlog is, and the
tile shows "50 pending suggestions" whether there are 50, 52, or 500.

Measured on a throwaway `fromHead` clone (`node harness/probe_dashboard_counts.mjs`,
2026-07-10): minted **52** pending folk suggestions, then issued the Dashboard's
exact call:

| step | result |
|---|---|
| S0 control | pending queue empty before minting |
| mint | 52 pending suggestions land (202 each) |
| `GET /v1/queue?status=PENDING` (no limit) | **50 items returned, plus a `cursor`** |

So the tile would read **"50 pending suggestions"** while **52** await moderation,
and it stops moving until the backlog is worked below 50.

## Root cause

Three facts combine:

1. **The page caps at 50 and the Dashboard never pages past it.** `suggest/review.go:131-132`
   defaults `QueueQuery.Limit` to 50 (`if q.Limit <= 0 || q.Limit > 200 { q.Limit = 50 }`),
   and `:167-168` sets `page.Cursor` once the page fills. `loadPending`
   (`Dashboard.svelte:95-100`) makes one call and reads `page.items.length`,
   ignoring the cursor.

2. **There is no total to read instead.** `QueuePage` is `{ Items, Cursor }` with
   **no count field** (`suggest/review.go:118-121`). Getting the true number
   requires walking every cursor page.

3. **The client cannot even widen the page.** The HTTP handler
   (`backend/httpapi/review_handlers.go:69-74`) builds `QueueQuery` from
   `status/scheme/provenance/type/cursor` and **never reads a `limit` query
   param**, so `QueueQuery.Limit` is always 0 → 50. `api.ts fetchQueue` accepts a
   `limit` argument, but the server drops it (measured: `?limit=2` over 3 pending
   still returned all 3). The Dashboard is stuck with the first 50.

The two other `attention` tiles that also count a list -- Duplicate groups
(`(res.groups ?? []).length`, `:134`) and Withdrawals (`(res.works ?? []).length`,
`:138`) -- are **currently correct**, because `GET /v1/duplicates` and
`GET /v1/withdrawn` return the whole list unpaginated (`maintenance_handlers.go:257-281`,
`:192-206`). They share the anti-pattern (count the returned array rather than ask
for a count) and would silently cap the same way if those endpoints ever gained
pagination, but only the queue is paginated today, so only "Pending review" is wrong.

## Why it matters

The tile carries `attention: true` and is styled as a call-to-action
(`Dashboard.svelte:157, 287`): it exists precisely to tell a moderator how much
work is waiting. Capping it at 50 tells a moderator with a 300-item backlog that
there are 50, and the number does not fall as they clear items until they are
below 50 -- the opposite of the signal the tile is for. It is the same family as
115 / 261 / 300 / 313: a number the UI presents as authoritative is quietly bounded
by an implementation detail (here, a page size) the reader cannot see.

## Expected

The tile should report the true pending count, or make its boundedness visible.
Options:

- Add a count to the queue: either a `Total` on `QueuePage` (a `COUNT`-style read),
  or a dedicated `GET /v1/queue/count`. `Stats()` already rolls suggestion activity
  for the "Editing activity" section, so a pending count has precedent.
- Or, cheaply and honestly, render "50+" when `page.cursor` is non-empty, so the
  number never claims to be exact when it is not.

Either way, "50 pending suggestions" must not be shown when 52 are pending.

## Repro

```
node harness/probe_dashboard_counts.mjs   # 3/3: mints 52, shows the no-limit queue returns 50 + a cursor
node harness/retest.mjs                    # check t328 (STILL-BROKEN)
```

Both run on a throwaway `fromHead` clone: mint 52 pending folk suggestions (each
with a distinct `Cloudfront-Viewer-Address` so the per-supporter rate limit sees
different patrons), then issue the Dashboard's `GET /v1/queue` and confirm it
returns exactly 50 items and a cursor. Folk terms are never published; the clone
is discarded. Nothing touches :8481.
