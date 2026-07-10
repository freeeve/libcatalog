# 297 -- editor drafts are keyed only by user so a work can hold several and the resume banner offers whichever has the lowest random id

Filed from libcat-e2e on 2026-07-10 (cross-repo ask).

`editor.ts:130-131` names the invariant the editor relies on:

> *"Editing fresh past the resume offer adopts the **(work, user) draft slot** rather than
> piling up a second draft."*

Nothing owns that slot.

```go
func draftKey(email, id string) store.Key {                    // drafts_handlers.go:26-28
	return store.Key{PK: "DRAFT#" + email, SK: id}
}
…
suffix := make([]byte, 8)
_, _ = rand.Read(suffix)                                       // :40-41
d.ID = hex.EncodeToString(suffix)
rec := store.Record{Key: draftKey(id.Email, d.ID), …}
db.Put(r.Context(), rec, store.CondIfAbsent)                   // :46
```

**The work id is never part of the key**, and `CondIfAbsent` guards only the freshly minted
random id, which is absent by construction. `POST /v1/drafts` therefore never conflicts, and
one work can hold any number of drafts for one user.

The client resolves the slot by scanning, and takes the first match:

```ts
const { drafts } = await fetchDrafts();                              // editor.ts:151
pendingDraft = drafts.find((d) => d.workId === workId) ?? null;      // :152
```

`GET /v1/drafts` returns store order, which both stores define as ascending sort key
(`mem.go:135`; `dynamo.go:201` `ScanIndexForward`) -- and the sort key is that random hex. So
when a work holds two drafts, **the one you are offered is the one whose identifier happens
to sort first.** Not the newest. Not the largest. The luckiest.

## Symptom

Measured on the running playground (`:8481`), as one librarian.

**Three drafts, one work, one user:**

```
POST /v1/drafts {workId: w0cfnsjg6micju, ops: 1}  -> 201
POST /v1/drafts {workId: w0cfnsjg6micju, ops: 7}  -> 201
POST /v1/drafts {workId: w0cfnsjg6micju, ops: 3}  -> 201
GET  /v1/drafts  -> the work holds 3 drafts
```

**And it is reachable without touching the API.** Two ordinary editor sessions of the same
librarian -- two devices, or two tabs -- both opened on the same work, each staging **one**
edit and never pressing Save:

```
after both loaded, drafts = 0
each stages one edit, autosave fires (AUTOSAVE_MS = 3000)
drafts for w0cfnsjg6micju: 2
  576fe1e3af3d35d9  ops=1  08:05:21.165
  7861791a23ae165a  ops=1  08:05:20.522
```

Neither session ever saw the other's draft -- `load()` ran before either autosave landed --
so each took the `createDraft` branch. Note the order: `576f…` is listed **first** and is the
**newer** of the two. The list is not in time order.

**The resume banner then offers the loser.** With an older 1-edit draft and a newer 6-edit
draft on the same work, in a fresh browser with no local mirror:

```
drafts: 6a1ab6…  1 edit   (older)
        8b3af8…  6 edits  (newer)

banner: "You have a draft for this work (1 edits, saved 7/10/2026, 4:07:37 AM)."
button: "Resume draft (1 edits)"
```

The six-edit draft is invisible. There is no UI that lists it, and nothing deletes it: `save()`
(`editor.ts:238`) and `discardDraft()` (`:305`) both remove only `state.draftId`, the one that
was adopted. The sibling sits in `DRAFT#<email>` until its **90-day TTL** expires -- and gets
offered again on the next open, whenever its id sorts first.

## Secondary: every editor open downloads every draft body

`GET /v1/drafts` (`drafts_handlers.go:53-67`) takes no `workId` and projects nothing. It
returns every draft the user owns, each with its full `body` -- capped at 1 MB by
`MaxBytesReader` (`:36`), retained 90 days. `editor.ts:151` calls it on **every work-editor
open** to find the one draft it wants.

```
GET /v1/drafts with 0 drafts    ->     14 bytes
GET /v1/drafts with 12 drafts   -> 482,595 bytes    (~40 KB of body each)
GET /v1/drafts?workId=<id>      -> 12 drafts        (the parameter is ignored)
```

A cataloger who leaves a draft on fifty works transfers all fifty bodies to open the
fifty-first.

## Secondary: "1 edits"

```svelte
You have a draft for this work ({$session.pendingDraft.body?.ops?.length ?? 0} edits, saved   WorkEditor.svelte:201
Resume draft ({$session.pendingDraft.body?.ops?.length ?? 0} edits)                           WorkEditor.svelte:205
```

Neither pluralizes, and a one-edit draft is the common case. `editor.ts:236` in the same
feature gets it right: `` `Saved ${n} edit${n === 1 ? "" : "s"}` ``.

## Root cause

`backend/httpapi/drafts_handlers.go:26-28`. The store key is `DRAFT#<email>` / `<randomId>`.
Drafts are scoped to a **user**, and the code that reads them wants them scoped to a **(user,
work)** pair. Every symptom above follows: no uniqueness to enforce, no key to filter a list
by, no ordering but the id's.

The 90-day TTL and the cross-user isolation are correct and well tested (`404` rather than
`403` throughout, no existence leak). This is the one axis nobody keyed on.

## Why it matters

**Silent loss of work.** A cataloger stages six edits on a laptop, opens the same record on a
desk machine, and is offered a one-edit draft from Tuesday. Resuming it and saving discards
nothing visibly -- the six-edit draft simply never appears. The only signal that it existed is
that the banner's edit count is lower than they remember, which is exactly the kind of thing a
person doubts rather than reports.

**Two tabs is not an exotic case.** It is the scenario the module doc names as the reason
server drafts exist at all: *"The server draft (3s autosave) is the durable cross-device
copy"* (`localdraft.ts:3-4`). The 3-second autosave debounce is precisely the window in which
two sessions both see no draft.

**Discarding does not clear the work.** `discardDraft()` deletes the offered draft, so the
banner returns on the next open with the sibling. To a cataloger that reads as "Discard
doesn't work."

**The list endpoint is an unbounded fan-out** on the hot path of opening any record.

## Expected

- **Key the draft by (user, work).** `SK: "W#" + workId` makes the slot real: `Put` with
  `CondIfAbsent` becomes a genuine uniqueness guard, `GET /v1/drafts/{workId}` is a point
  read, `find()` disappears, and the list can be a projection without bodies. The draft id
  stops being an identifier and becomes a coincidence. This also makes `editor.ts:130-131`'s
  comment true.

  It is a breaking change to the URL shape (`/v1/drafts/{id}`), and the drafts in flight are
  90-day scratch state -- expiring them is defensible.

- **Or, if the id must stay opaque:** enforce uniqueness on write (reject or replace an
  existing draft for the same `(email, workId)`), and make `load()` pick deterministically --
  `max(updatedAt)`, not `find()`. Add `?workId=` to the list route and drop `body` from the
  list projection. That is three changes where the re-key is one.

- **Either way, pick the newest.** `drafts.find(…)` should be
  `drafts.filter(…).sort(byUpdatedAtDesc)[0]` even after uniqueness lands, because it costs
  nothing and fails safe.

- **Pluralize the banner** (`WorkEditor.svelte:201,205`), the way `editor.ts:236` does.

- **Test it.** There is no `drafts_handlers_test.go`. The only Go coverage is a single-draft
  CRUD walk inside `records_handlers_test.go:263-291` -- create, update, list, get, delete,
  404 -- which one draft can never fail. Assert the invariant the client already documents:
  two `POST`s of the same `workId` must not yield two drafts.

## Repro

```bash
cd ~/libcat-e2e && node harness/probe_draft_slot.mjs   # W2, W4, W5, W6, W7
cd ~/libcat-e2e && node harness/retest.mjs             # check t297
```

Read/write against the playground on `:8481`. Drafts are per-user scratch state, not catalog
data. The UI half **stages** a sentinel title and never presses Save, so no work's grain is
written -- verified after the run (`zz-e2e` appears nowhere in the target work's N-Quads).
Every draft present before the probe starts is recorded and left alone; only drafts the probe
created are deleted, and `W8` asserts both halves of that.

Its controls carry the argument. `W1` round-trips a single draft, so `W2` is about uniqueness
and not a broken route. **`W3` shows two consecutive `GET`s return the identical order**, so
`W4` is a missing key rather than a race. `W4` refuses to run until it has built a pair whose
first-by-id draft is the *older* one -- otherwise "the banner offers the newest" could pass by
luck, on a coin flip. `W8` asserts the pre-existing drafts survived.

By hand:

```bash
TOK=...   # a librarian token
WORK=...  # any work id

# Three drafts, one work, one user. Each POST returns 201 and a fresh random id.
for n in 1 7 3; do
  jq -nc --arg w "$WORK" --argjson n "$n" \
    '{workId:$w, body:{baseEtag:"e0", ops:[range($n)|{resource:"work",path:"tags",action:"add",value:{v:"x"}}]}}' \
  | curl -s -XPOST -H "Authorization: Bearer $TOK" -H 'content-type: application/json' \
      -d @- localhost:8481/v1/drafts | jq -r .id
  sleep 1
done

curl -s -H "Authorization: Bearer $TOK" localhost:8481/v1/drafts \
  | jq -r '.drafts[] | "\(.id)  ops=\(.body.ops|length)  \(.updatedAt)"'
```

The list comes back in ascending id order, not time order. Now open `#/works/$WORK` in a
browser that has no `lcat-localdraft-$WORK` entry: the banner offers the draft printed
**first**, whatever its edit count or age.

## Outcome

Shipped in **v0.141.0** (`9d7c8da`) -- a **minor**: the draft list response shape
changes (bodies dropped) and drafts are re-keyed, so a consumer (the e2e harness) has
something to adopt. Took your first option, the re-key, because -- as you said -- it is
one change where the patchwork is three.

### The slot is real now

`drafts_handlers.go`: `draftKey(email, workID) = {PK: "DRAFT#"+email, SK: "W#"+workID}`.
The draft's public `id` **is** the work id. Every symptom follows from that one key:

- **One draft per (user, work).** POST keys by work and **upserts** (`CondNone`), so a
  second tab's autosave lands last-writer-wins on the shared slot instead of piling up
  a rival draft. I chose replace over reject (your option-2 phrasing "reject *or*
  replace") so a second autosave never errors.
- **`load()` is a point read.** `editor.ts` now calls `fetchDraft(workId)`
  (`GET /v1/drafts/{workId}`) instead of listing every draft and `find()`-ing. No
  ordering to be at the mercy of, no rival to pick between -- there is at most one.
- **The list stops fanning out.** `GET /v1/drafts` drops `body` from its projection
  (the struct field is now `omitempty`, the handler nils it), so opening a record no
  longer downloads every other record's draft body. The point read carries the body
  for the one draft the editor wants.
- **A POST with no `workId` is refused** (400) rather than keyed to an empty slot.

`editor.ts:130-131`'s comment ("adopts the (work, user) draft slot") is finally true --
the slot it names now exists.

### The banner

`WorkEditor.svelte` pluralizes via `{@const draftOps}`: **"1 edit"**, "2 edits". Same
shape `editor.ts:236` already used.

### Tests

`TestDraftSlotIsUniquePerWork` (Go): three POSTs of one work → **one** draft whose id is
the work id, the list carries no body, the point read returns the **last** write, a
different work gets its own slot, and a workId-less POST is 400. `TestDraftsCRUD` still
passes (its `d.ID` is now the work id). The client `editor.test.ts` `loadSession` helper
switched to mocking the point read; all 10 editor flows green. Full UI suite 322/322,
backend all green, `gofmt -s` clean, `svelte-check` 0 errors.

### Verified end to end on a throwaway :8491

```
POST wtest1 x3 (ops 1,7,3)  -> all return id=wtest1
GET  /v1/drafts             -> 2 drafts (wtest1, wtest2), ids=workIds, no body
GET  /v1/drafts/wtest1      -> ops=3   (the LAST write, not the luckiest id)
POST no workId              -> 400
```

And on `:8481`, seeded a one-op draft and read the real banner: **"You have a draft for
this work (1 edit, saved …)"** and **"Resume draft (1 edit)"** -- singular. Draft
deleted afterward (204, then 404); the playground is left as found.

### Adoption note (breaking bits)

- **In-flight drafts are re-keyed.** Old random-id drafts age out via the existing
  90-day TTL; they are scratch state, and expiring them is defensible (your words).
- **`GET /v1/drafts` no longer includes `body`** -- a consumer that read edit counts
  off the list must switch to the point read `GET /v1/drafts/{workId}`. The `t297`
  probe's `.body.ops.length`-off-the-list assertion moves to the point read.
- The URL shape `/v1/drafts/{id}` is unchanged; `{id}` is now the work id, which is
  what the POST/point-read already return.
