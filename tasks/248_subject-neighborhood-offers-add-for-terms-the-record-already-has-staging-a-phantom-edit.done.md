# 248 -- subject neighborhood offers Add for terms the record already has, staging a phantom edit

Filed from libcat-e2e on 2026-07-09 (cross-repo ask).

## Symptom

Open the shipped demo record **"River of teeth"** (`w1dh6vtir43o8i`) and expand its
first subject chip, `Bisexual people` (HOMOSAURUS). The neighborhood panel offers an
**Equivalents** row:

```
equivalent row: "Bisexual people   LCSH   Replace   Add"
```

That LCSH term is *already a subject of this very record* -- it is the third chip in
the same list. The panel gives no sign of it. Clicking **Add**, the obvious action for
a cataloger doing the homosaurus->LCSH crosswalk the panel exists for (tasks/072),
stages an addition of a subject the record already carries. The subject list then shows
it **twice**:

```
value                :: Bi+ people ›Bisexual people   HOMOSAURUS ▾   editorial   Remove
value                :: Bisexual people   LCSH ▸                     editorial   Remove
value pending-added  :: Bisexual people   LCSH        adds on save   ✕ undo
```

The staged op names the IRI the record already has:

```json
{"resource":"work","path":"subjects","action":"add",
 "value":{"v":"http://id.loc.gov/authorities/subjects/sh93003390","iri":true}}
```

and the grain already carries exactly that edge, once:

```
<#w1dh6vtir43o8iWork> <http://id.loc.gov/ontologies/bibframe/subject>
  <http://id.loc.gov/authorities/subjects/sh93003390> <editorial:> .
```

The record is not corrupted -- `subjects` stays at 6, no duplicate edge is written. The
damage is to the cataloger:

- the subject reads as listed twice, one of them "adds on save";
- the editor goes **dirty**, so a draft is autosaved (`Resume draft (1 edits)` survives
  a reload) and **Clone is disabled** ("Disabled while edits are staged");
- saving writes a history entry for an edit that changes nothing (see **249**, filed
  alongside this: the empty-diff save still writes `RECORD_EDIT`).

Dry-run confirms the addition is not an addition. `POST /v1/works/{id}/ops` with
`dryRun`, against the already-present subject, versus a genuinely new one:

```
add a subject the work ALREADY has -> added 1, removed 0
  <…sh93003390> <skos#prefLabel> "Bisexual people"@en <authority:lcsh> .

add a NEW subject (control)        -> added 2, removed 0
  <#w1dh6vtir43o8iWork> <bibframe/subject> <…sh85021262> <editorial:> .
  <…sh85021262> <skos#prefLabel> "Cats"@en <authority:lcsh> .
```

The control gains a `bf:subject` edge. The phantom add gains **no `bf:subject` edge at
all** -- only a label quad in the `authority:lcsh` graph. Nothing about the record's
subjects changes.

## Root cause

`backend/ui/src/components/SubjectNeighborhood.svelte:14-20` -- the component's props
are `{ term, onreplace, onadd }`. It never learns which terms the record already has,
so every neighbor and every equivalent gets an unconditional Replace/Add pair
(`:105-118` for Equivalents, `:130-142` for Broader/Narrower/Related/Siblings).

`backend/ui/src/components/ProfileForm.svelte:503-509` mounts it passing only `term`,
though the parent is rendering the field's stored values in the very same `{#each}` and
therefore already holds the set of current subject IRIs.

`backend/ui/src/components/ProfileForm.svelte:315-318` -- `addSubject()` stages the op
unconditionally:

```ts
function addSubject(path: string, next: Term): void {
  pickedLabels[next.id] = bestLabel(next);
  onstage({ resource, path, action: "add", value: { v: next.id, iri: true } });
}
```

`SubjectLookup` has the same gap, so this is a consistent omission rather than an
inconsistency between the two surfaces.

Note that clicking **Add** a second time silently *unstages* the op -- documented toggle
semantics (`backend/ui/src/lib/ops.ts:11-13`), so the pending chip disappears. That is
intended behaviour and not part of this report, but from a button labelled "Add" it
compounds the confusion.

## Why it matters

The crosswalk panel's entire purpose is to pull the LCSH equivalent onto a record that
has the homosaurus term. Records where a previous cataloger already did that are exactly
the records where the panel is most likely to be opened -- and there the panel's headline
action is a no-op dressed as an edit. "River of teeth" is a record that ships with the
demo, so the first cataloger to try the feature on it meets this.

For a catalogers' tool the cost is trust: the panel says the subject will be added, the
record says it is already there, and the two are the same term.

## Expected

The neighborhood and equivalents rows should mark a term the record already carries --
disable **Add** (keeping **Replace** meaningful, since Replace still removes the expanded
term), or render "already a subject" in place of the button. Passing the field's current
IRIs into `SubjectNeighborhood` is enough; `ProfileForm` already has them.

Belt and braces: `addSubject()` should not stage an add for a value already present.

## Repro

```bash
cd ~/libcat-e2e && node harness/probe_neighborhood.mjs
```

Or by hand: sign in to :8481, open `#/works/w1dh6vtir43o8i`, expand the first subject
chip, and click **Add** on the `Bisexual people (LCSH)` equivalent. The chip list shows
the term twice. Delete the autosaved draft afterwards:
`DELETE /v1/drafts/{id}` (this report's staging was cleaned up that way).

## Outcome

Fixed in **v0.100.0** (`1e0b45a`). Your root cause was exact, down to the line
numbers, and your suggested fix is the one taken.

`ProfileForm` already held the field's values in the very `{#each}` that mounts
the panel, so it now passes their IRIs down as `present`. `SubjectNeighborhood`
renders **"already a subject"** in place of Add for a term in that set, on both
the Equivalents rows and the Broader/Narrower/Related/Siblings rows (a shared
`actions` snippet -- the two markup blocks had drifted into duplicates).

**Replace stays offered**, per your note: it still removes the expanded subject,
which is how a cataloger drops the source term once the crosswalk target is on
the record. Its tooltip now says so. Replacing *with* an already-present term
stages only the removal, not a phantom add.

Belt and braces, as you asked: `addSubject()` and `replaceSubject()` both refuse
to stage an add for a value already present, so the guard holds behind the
button and not only in front of it. That also covers `SubjectLookup`, which you
noted has the same omission -- it stages through the same functions.

Two decisions where the report left room:

- **A value staged for removal counts as absent**, so Add comes back for it. A
  cataloger mid-way through "drop this and re-add it" is doing something real.
- **The toggle-off-on-second-click behaviour is untouched**, as you scoped it.
  With Add gone for present terms, the confusing case that made the toggle look
  like a bug can no longer arise from this panel.

`backend/ui/src/lib/subjects.ts` (`presentIRIs`, `wouldChange`) carries the
logic so it is unit-tested directly rather than only reachable through the DOM:
9 tests in `subjects.test.ts`, including set/clear redefining the whole value
set, and literals ignored as not crosswalkable.

### Verification

`node harness/probe_neighborhood.mjs` -- your probe, unmodified:

    N5  248: Add is disabled or marked for an already-present term  PASS
        row="Bisexual people LCSH Replace already a subject";
        addButton=false disabled=false marked=true
    CLEAN  0 draft(s) swept; no record was saved

The editor no longer goes dirty, so nothing is autosaved and Clone stays
enabled. `harness/retest.mjs`: **STILL-BROKEN: none** across 35 tracked
regressions. `svelte-check` clean; 252 UI tests pass.

Filed alongside, **249** is fixed too: an ops save with an empty diff no longer
writes a `RECORD_EDIT` row. It was reachable independently of this bug, and its
`PUT` twin was not reported at all.
