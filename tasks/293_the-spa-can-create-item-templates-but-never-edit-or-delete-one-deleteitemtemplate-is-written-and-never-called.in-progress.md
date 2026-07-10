# 293 -- the SPA can create item templates but never edit or delete one; deleteItemTemplate is written and never called

Filed from libcat-e2e on 2026-07-10 (cross-repo ask).

Item templates (tasks/069) have four routes. All four are live, and all four are correct:

```
GET    /v1/item-templates        batch_handlers.go:129
POST   /v1/item-templates        batch_handlers.go:138
PUT    /v1/item-templates/{id}   batch_handlers.go:152
DELETE /v1/item-templates/{id}   batch_handlers.go:166
```

The SPA reaches two of them.

```
$ grep -rn 'deleteItemTemplate' backend/ui/src
backend/ui/src/lib/api.ts:747:export function deleteItemTemplate(id: string): Promise<void> {

$ grep -rn 'updateItemTemplate' backend/ui/src
$        # nothing
```

`deleteItemTemplate` was written, exported, and is called by nobody. `updateItemTemplate` was
never written at all. `ItemsPanel.svelte` -- the only component in the SPA that knows templates
exist (`grep -rln ItemTemplate --include=*.svelte` returns it and nothing else) -- imports
`createItemTemplate` and `fetchItemTemplates`, and offers a `<select>`, an **Apply**, a **Save
row as template**, and a **…shared**.

**A librarian can create item templates and can never rename, edit, or remove one.** The
dropdown only grows. Clicking `…shared` publishes a row to every colleague permanently -- see
tasks/292 for why not even an admin can clean it up afterwards.

The sibling settles what "done" looks like. **Macros ride the same `owned.go` engine** and have
the same four routes, and they got a whole screen: `Macros.svelte:8` imports `createMacro`,
`updateMacro`, `deleteMacro`, `fetchMacros`, and `:189` renders Edit and Delete per row, guarded
by `{#if m.owner === me && !readOnly}`. Item templates got half of that.

## Symptom

`ItemsPanel.svelte:197-213`, the entire template surface:

```svelte
{#if !readOnly}
  <p class="acts">
    <button class="button button--quiet mini" onclick={add}>Add item</button>
    {#if templates.length > 0}
      <select class="mini-select" aria-label="Item template" bind:value={templateId}>
        <option value="">template…</option>
        {#each templates as t (t.id)}
          <option value={t.id}>{t.label}{t.shared ? " (shared)" : ""}</option>
        {/each}
      </select>
      <button class="button button--quiet mini" onclick={applyTemplate} disabled={!template}>Apply</button>
    {/if}
    {#if items.length > 0}
      <button class="button button--quiet mini" onclick={() => void saveAsTemplate(false)}>Save row as template</button>
      <button class="button button--quiet mini" onclick={() => void saveAsTemplate(true)}>…shared</button>
    {/if}
    <button class="button mini" onclick={() => void save()} disabled={busy || !dirty}>Save items</button>
```

No Delete. No Edit. No Rename. The panel knows perfectly well how to offer a delete affordance
-- `:193` renders a **Remove** button for every *item* row, four lines above. It just never
offers one for a template.

The routes themselves are complete. Driven live against the playground on `:8481`, as the owner:

```
POST   /v1/item-templates            -> 201   (owner forced to the caller)
PUT    /v1/item-templates/{id}       -> 200
DELETE /v1/item-templates/{id}       -> 204
```

So the server offers a full lifecycle and the client uses the first half of it.

## Secondary: `barcodeWidth` is a field no UI can set

`ItemTemplate.BarcodeWidth` (`itemtemplates.go:19`) is the zero-padded counter width for bulk
add -- prefix `"B-"` with width 6 gives `B-000001`. It is validated server-side (0-12,
`validateItemTemplate:63`), stored, round-tripped, declared in TypeScript (`types.ts:494`,
`api.ts:762`), and covered by unit tests (`itemtemplates_test.go:26` stores `BarcodeWidth: 6`;
`:68` asserts 44 is rejected).

`ItemsPanel.svelte` **reads** it, exactly once:

```svelte
barcodeWidth: template?.barcodeWidth,     // :126, in the bulkAddItems request body
```

and writes it nowhere. `saveAsTemplate` (`:95-102`) posts `label`, `callNumber`, `location`,
`note`, `barcodePrefix`, `shared` -- no width. There is no width input anywhere: the bulk row
(`:219-225`) has a `Copy count` and a `Barcode prefix` and stops there. And with no `PUT` wired,
a template created through the UI has `barcodeWidth` undefined **forever**.

`bulkAddItems` therefore always falls back to `defaultBarcodeWidth = 4`
(`httpapi/items_bulk.go:23,90-92`). A library whose barcodes are six digits cannot express that
anywhere in the product, though the field exists at every other layer.

The prefix shows what the width was supposed to look like. `applyTemplate:83` copies
`template.barcodePrefix` into the `bulkPrefix` state that backs the input, so the prefix becomes
visible, editable, and re-savable. The width has no state and no input: `bulk()` reads it
straight off the currently-selected template at request time. So it also vanishes if the
cataloger clears the `template…` select before pressing **Add copies**, while the prefix -- now
living in the form -- stays put. Two fields of one record, one wired through the form and one
wired around it.

## Root cause

`backend/ui/src/lib/api.ts:736-749` -- three functions where the sibling has four:

```ts
/** The caller's item templates plus every shared one (librarian, tasks/069). */
export function fetchItemTemplates(): Promise<{ templates: ItemTemplate[] }> { … }

/** Saves an item template; shared templates are library-wide (librarian). */
export function createItemTemplate(t: ItemTemplate): Promise<ItemTemplate> { … }

/** Removes an owned item template (librarian). */
export function deleteItemTemplate(id: string): Promise<void> {          // never imported
  return call("DELETE", `/v1/item-templates/${encodeURIComponent(id)}`);
}
```

`ItemsPanel.svelte:7-15` imports `bulkAddItems`, `createItemTemplate`, `fetchItems`,
`fetchItemTemplates`, `putItems`. Not `deleteItemTemplate`.

The component's own header describes the shape it grew into: *"Item templates pre-fill rows and
bulk add generates N copies with sequential barcodes (tasks/069)"* (`:4-5`). Pre-filling is all
it does. Nothing owns the template lifecycle, because item templates never got the screen macros
got -- the items panel is a holdings editor with a dropdown bolted on.

## Why it matters

**Templates are per-librarian state that accumulates without bound.** Every typo'd label, every
one-off experiment, every `…shared` misclick is permanent from inside the product. The only
remedy is `curl` -- and for a shared row whose owner has left, not even that (tasks/292).

**The dead `deleteItemTemplate` is the tell.** Somebody wrote the client function, gave it a doc
comment, and no component ever called it. That is a feature designed complete and shipped
half-wired. Nothing -- no test, no lint, no type error, no build warning -- notices an exported
API function with zero callers, so the gap is invisible to every check the repo runs.

**`barcodeWidth` is worse than absent: it is present everywhere except where it could be
entered.** It has a Go field, validation, a store round-trip, two unit-test assertions, a
TypeScript type, and a use site in the outgoing request body. It has no input. A field that
exists at six layers and is settable at none reads, to a reviewer checking coverage, as done.

## Expected

- **Give item templates the lifecycle macros have.** The smallest honest version is a Delete
  control beside the `<select>`, owner-only, mirroring `Macros.svelte:189`, calling the
  `deleteItemTemplate` that is already written and already tested on the server. Rename and edit
  want `updateItemTemplate`, which does not exist in `api.ts` and should -- the `PUT` route is
  already there.

- **Add a barcode-width input** next to the barcode-prefix input at `:222`, persist it through
  `saveAsTemplate`, and have `applyTemplate` copy it into that state the way `:83` copies the
  prefix. Then the width behaves like the prefix instead of like a hidden argument.

- **Or delete `deleteItemTemplate` and the `PUT` route**, drop `BarcodeWidth` with them, and say
  plainly that templates are immutable and append-only. That is a defensible product decision.
  The current state is not a decision; it is a gap that looks like one.

- **Add a check for exported API functions with no callers.** `deleteItemTemplate` would have
  been caught on the day it landed, and so would the next one. `backend/ui/src/lib/api.ts` is a
  single file; a `knip`-style unused-export pass over it is cheap.

## Repro

```bash
cd ~/libcat-e2e && node harness/probe_owned_shared.mjs   # O7, O8
cd ~/libcat-e2e && node harness/retest.mjs               # check t293
```

Read/write against the playground on `:8481`. The probe drives the real Work editor with
Playwright, opens the items panel, saves a row as a template, and enumerates the controls the
panel then offers for it; it also records the request body the SPA actually sent. Every record
it creates is labelled `zz-e2e-…` and is removed afterwards through the API.

Its controls carry the argument. `O7`'s control is that the panel **does** render the template
`<select>` and its **Apply** after the save -- so "there is no Delete control" is a measured
statement about a rendered panel, not about a panel that failed to render. `O8`'s control is
that the create request the SPA sent **does** carry `barcodePrefix` -- so the absent
`barcodeWidth` is an omission in that body, not a body the probe failed to capture.

By hand, in the source:

```bash
grep -rn 'deleteItemTemplate' ~/libcat/backend/ui/src   # one hit: the definition
grep -rn 'updateItemTemplate' ~/libcat/backend/ui/src   # none
grep -rn 'barcodeWidth' ~/libcat/backend/ui/src/components/ItemsPanel.svelte   # one hit: :126, a read
```
