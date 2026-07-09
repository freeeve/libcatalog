# 199 -- work editor discards staged edits silently: no guard on in-app navigation and no beforeunload prompt

Filed from libcat-e2e on 2026-07-09 (cross-repo ask).

## Symptom

Stage an edit in the work editor -- e.g. add the tag `Fiction.`, so the chip
reads `Fiction. adds on save ✕ undo` and the **Save** button becomes enabled --
then leave the record without saving. The staged work is thrown away with no
warning of any kind:

| leaving by | guard | result |
|---|---|---|
| clicking the **Works** nav link | none (no dialog, no modal) | **silent discard** |
| closing / reloading the tab (`beforeunload`) | none | **silent discard** |

Verified state at the moment of navigation: `Save [ENABLED]`,
`Save staged edits as macro [ENABLED]`, chip shows `adds on save`. The app knows
perfectly well it is dirty; it just does not defend the work.

## Why it matters

This is the classic cataloging data-loss trap. A cataloger stages several field
edits on a record, clicks *Works* to check something in the result list, and the
edits are gone -- with no prompt, no toast, no draft. There is a `#/drafts`
concept and an explicit `Save`, which makes the silent discard all the more
surprising: the app has somewhere to put the work and chooses to drop it.

The editor already tracks dirtiness precisely (Save enables/disables, chips
carry `adds on save` + `✕ undo`), so the signal needed to guard is present.

## Expected

1. **In-app navigation** with staged edits prompts: *"Discard unsaved changes to
   this record?"* -- Discard / Keep editing (and ideally *Save and continue*).
2. **`beforeunload`** is registered while dirty, so a tab close or reload gets
   the browser's native "Leave site?" prompt.
3. Ideally, staged edits autosave to a draft (`/v1/drafts`) so a discard is
   recoverable rather than terminal.

## Repro

```sh
# libcat-e2e
node ui/verify_dirty3.mjs
# staged edit present: {"addsOnSave":true,"saveBtn":[... "Save [ENABLED]"]}
# (1) in-app nav: dialog=false modal=0 left=true -> SILENT DISCARD
# (2) beforeunload (staged=true): dialog=false   -> NO GUARD
```

Note: staging a tag requires picking an existing suggestion, because a novel tag
cannot be entered at all -- see task 198.

## Not bugs (checked while auditing)

- Focus ring is present and strong on every tabbable (`solid 3px`); an earlier
  reading of "no focus ring" was a bad probe (`getComputedStyle(el,
  ':focus-visible')` does not work).
- **Suppress** does not mutate on click -- it stages, leaving server visibility
  untouched. So it needs no confirm dialog of its own; guard 199 covers it.
- Accessible names are present on every control on `/works`, `/batch`,
  `/exports`; landmarks are correct (one `h1`, `nav`, `main`).
