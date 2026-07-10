# 333 -- review queue Type filter omits CONCERN, so moderators cannot isolate patron problem-reports

Filed from libcat-e2e on 2026-07-10 (cross-repo ask).

## Symptom

The review-queue screen (the only place concerns are triaged) has a Type filter dropdown
that offers only **ADD** and **REMOVE**. There is no **CONCERN** option, so a moderator
cannot narrow the queue to the anonymous "report-a-problem" concerns patrons file
(tasks/210) -- they can only be seen mixed into the unfiltered ("any") list, below term
ADD/REMOVE suggestions.

Measured on a throwaway HEAD clone: submit one concern through the public endpoint, then
query the queue the way the dropdown would:

```
POST /v1/concerns {workId, note, workTitle, challenge}      -> 202
GET  /v1/queue?type=CONCERN   -> 1 item, type=CONCERN        (backend serves the filter)
GET  /v1/queue?type=ADD       -> 0 items                     (type filtering is real)
```

So the server fully supports `?type=CONCERN`; the UI simply never sends it.

## Root cause

`backend/ui/src/screens/Queue.svelte:26`

```
const TYPES = ["ADD", "REMOVE"];
```

CONCERN is missing from this hand-maintained array, even though the SAME component and the
type system already know about it:

- `Queue.svelte:286,288-291,309` render a CONCERN row specially (a `chip--concern`, the
  freetext note instead of a term, resolve/dismiss controls).
- `backend/ui/src/lib/types.ts:302` -- `export type SuggType = "ADD" | "REMOVE" | "CONCERN";`
- The queue handler passes the type straight through (`review_handlers.go:72`,
  `Type: suggest.SuggType(query.Get("type"))`) and `Queue` filters on it
  (`suggest/review.go`, `sg.Type != q.Type` -> skip), so `?type=CONCERN` returns exactly the
  concerns.

The Provenance filter, by contrast, lists all three backend constants
(`PATRON`/`PIPELINE`/`LIBRARIAN`), so only TYPES drifted when concerns were added in
tasks/210. There is no separate concerns worklist -- `grep -rni concern backend/ui/src`
finds only Queue.svelte and the type union, and the "worklist filters TypeConcern" comment
(`suggest/review.go:204`) refers to the *publisher's* worklist (concerns never publish), not
a UI screen. Queue.svelte is the one and only concern-triage surface.

## Why it matters

Concerns are anonymous problem reports about a work (wrong metadata, offensive content, a
bad cover) -- exactly the queue items a moderator most wants to find fast and clear. On a
busy catalog they are a minority of a queue dominated by term ADD/REMOVE pressure, and the
one control built to slice the queue by kind cannot select them. The feature is fully built
end to end (submit, index, render, resolve/dismiss) except for this one dropdown option, so
the reports are reachable only by scrolling the whole "any" list.

## Expected

Add `CONCERN` to the Type filter list so a moderator can filter to it, the same way ADD and
REMOVE already work:

```
const TYPES = ["ADD", "REMOVE", "CONCERN"];
```

(Deriving TYPES from the `SuggType` union in types.ts would also prevent the next drift.)

## Repro

```
# backend serves the filter the UI cannot send:
node ~/libcat-e2e/harness/probe_concern_filter.mjs   # mints a concern on a clone, asserts ?type=CONCERN returns it
# the UI omits the option:
grep -n 'const TYPES' ~/libcat/backend/ui/src/screens/Queue.svelte   # -> ["ADD", "REMOVE"]
```

Retest: `t330` in harness/retest.mjs drives the live Queue screen on :8481, opens the Type
`<select>`, and asserts a CONCERN `<option>` exists (STILL-BROKEN until it does), with a
backend control that `GET /v1/queue?type=CONCERN` answers 200.
