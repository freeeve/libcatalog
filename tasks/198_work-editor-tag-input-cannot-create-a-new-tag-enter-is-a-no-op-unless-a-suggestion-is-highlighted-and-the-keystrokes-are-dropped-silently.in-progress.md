# 198 -- work editor tag input cannot create a new tag: Enter is a no-op unless a suggestion is highlighted, and the keystrokes are dropped silently

Filed from libcat-e2e on 2026-07-09 (cross-repo ask).

## Symptom

In the work editor's **Tags** field a cataloger can only ever apply a tag that
already exists in the catalog. Typing a new tag and pressing Enter does nothing
at all -- no chip, no request, no error, no hint. The typing simply disappears.

Observed on the 8481 playground:

| input | suggestion menu | Enter | result |
|---|---|---|---|
| `zz-brand-new-tag` | closed (no matches) | pressed | **silently dropped** -- 0 network calls, no chip |
| `fic` | open (`Fiction.`, `Young Adult Fiction.`, …) | pressed | `Fiction. adds on save ✕ undo` -- correct |

## Root cause

`backend/ui/src/components/TagInput.svelte:78`

```ts
function onKeydown(ev: KeyboardEvent): void {
  if (!open) return;                    // (1) no menu -> Enter never handled
  ...
  } else if (ev.key === "Enter") {
    ev.preventDefault();
    const o = options[highlight];
    if (o) choose(o.tag);               // (2) only ever commits an existing option
  }
```

1. `open` is `options.length > 0` (`:68`). A novel tag matches nothing, so the
   menu never opens and the handler returns before it inspects the key.
2. Even with the menu open, Enter commits `options[highlight]` -- an existing
   tag. `choose()` is never called with the raw typed value, so no code path
   coins a new tag.

## Why it matters

Free tagging is the point of the Tags field -- the promotion workflow
(`#/promotions`, "Fold community tags into the catalog") presumes catalogers and
patrons mint new tags. Today the editor cannot mint one. Worse, it fails
*silently*: the cataloger types a tag, hits Enter, sees nothing happen, and has
no way to tell whether the app is broken or they did something wrong.

## Expected

Enter commits the typed value:

- menu open with a highlighted option -> commit that option (today's behaviour);
- otherwise -> commit the raw trimmed `q` as a new tag, when non-empty.

Drop the `if (!open) return;` early bail (or narrow it to the Arrow/Escape
branches) so Enter is always handled. Consider rendering a `Create tag "foo"`
row as the last option when `q` matches nothing, so the affordance is visible
rather than implicit.

## Repro

```sh
# libcat-e2e
node ui/verify_taginput.mjs
# (b) NEW tag typed -> menuOpen:false … Enter -> SILENTLY DROPPED, 0 network calls
# (a) prefix "fic"  -> menuOpen:true  … Enter -> "Fiction. adds on save ✕ undo"
```

## Related

- The staged edit this field produces is then discarded without warning on
  navigation -- see task 199.
