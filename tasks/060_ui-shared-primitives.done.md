# 060 -- Shared RowList and Modal primitives

## Context

Phase 2 of the admin UX overhaul. The list-with-selected-highlight idiom is re-implemented
in WorkSearch, Authorities, Queue, CommandPalette, VocabPicker, and TagInput; the modal
scrim/panel/focus-trap is re-declared in CommandPalette, VocabPicker, and KeyboardHelp;
BatchOps names saved queries through `window.prompt`. Every later phase (density, triage
screens) builds on one shared row/modal pair, so extraction comes first.

## Scope

- `components/Modal.svelte`: scrim + panel + `role=dialog`, focus on mount
  (`[data-autofocus]` else panel), Tab trap, Escape -> `onclose` (stopPropagation),
  opener-focus restore; `width` and `placement` (center|top) props.
- `components/RowList.svelte` (generic over T): `items`, bindable `selected`, `getKey`,
  `ariaLabel`, optional `onactivate`, optional keyboard `scope` (registers j/k/arrows and
  Enter with legend labels), row snippet `(item, i, selected)`, `empty` snippet/string;
  exports `move(delta)` with clamp + scrollIntoView. Selected style is the shared
  `.rowlist` CSS in app.css: hairline separators, inset accent rail (no layout shift).
- Migrate: CommandPalette, VocabPicker, KeyboardHelp onto Modal; WorkSearch, Authorities,
  Queue, CommandPalette, VocabPicker onto RowList (TagInput keeps combobox markup, adopts
  the shared look). Replace the BatchOps `window.prompt` with a Modal name form.

## Acceptance

- rowlist/modal unit tests (selection clamp, Enter activate, Escape/trap/focus-restore).
- a11y suite green with unchanged screen semantics; keyboard behavior identical.
- `npm run check`, `npm run test`, `npm run build` green.
