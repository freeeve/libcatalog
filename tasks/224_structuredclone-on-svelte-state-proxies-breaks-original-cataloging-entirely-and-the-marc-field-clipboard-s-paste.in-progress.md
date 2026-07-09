# 224 -- structuredClone on Svelte $state proxies breaks original cataloging entirely and the MARC field clipboard's paste

Filed from libcat on 2026-07-09 (cross-repo ask).

## Outcome

Fixed in v0.74.0 (commit 779644d), exactly per the diagnosis: pick()
and the clipboard readers snapshot before cloning (clipPush too,
defensively -- callers can hand a proxy), and loadTemplates no longer
reports a load failure for an error thrown after a 200 (pick() moved
out of the fetch's try). Regression tests: clipboard push/peek/at
round-trip including a real $state-proxy push (via screenState).

Verified with the filer's probe_newrecord.mjs against the rebuilt
8481: 8/8 PASS, including N8's cut-then-paste restoring the 008. (An
initial N4 fail was a batch-count race against this session's own
leftover verification batch, not the server: a refused stage was
confirmed batch-less by direct API check, and the rerun passes.)

## Symptom

Two cataloger-facing surfaces are dead, both from the same call. Measured on the
8481 playground through the real UI.

**1. `#/copycat/new` renders no editor at all.** The template dropdown and the
batch-label box appear; the MARC grid, the field clipboard pane, and the
`Stage for review` button never mount. Original cataloging cannot be started.

```
templates in the dropdown: 4          (GET /v1/copycat/templates -> 200, all 4 carry a record)
div.grid[aria-label="MARC fields"]: 0
button "Stage for review":          0
pageerror: Failed to execute 'structuredClone' on 'Window': #<Object> could not be cloned.
```

Picking a different template from the dropdown throws the same error and changes
nothing. On first load the throw is swallowed by `loadTemplates()`'s catch, which
reports `loading the templates failed` -- misleading, since the fetch returned 200.

**2. The MARC field clipboard cannot paste, and `alt+x` loses the field.** In the
work editor's MARC tab, `alt+c` (copy) is silent and `alt+v` (paste) throws:

```
rows: 5   after alt+c: no error   after alt+v: rows still 5
pageerror: Failed to execute 'structuredClone' on 'Window': #<Object> could not be cloned.
```

Cut is worse, because it deletes before the paste fails:

```
before cut:  001  008  041  245  336
after  cut:  001       041  245  336     (no error yet)
after paste: 001       041  245  336     pageerror: structuredClone ... could not be cloned
```

The 008 is gone from the editing surface and `alt+v` cannot bring it back. There
is no undo.

## Root cause

`structuredClone()` cannot clone a Proxy, and Svelte 5's `$state` deep-proxies
everything read out of it.

- `backend/ui/src/screens/NewRecord.svelte:60`
  ```js
  st.record = structuredClone(tpl.record);   // tpl came from `let templates = $state<CopycatTemplate[]>([])`
  ```
  `pick()` throws before assigning `st.record`, so the `{#if st.record}` block
  (grid, clipboard pane, Stage button) never renders.

- `backend/ui/src/lib/fieldClipboard.svelte.ts:20` (`clipPeek`) and `:26` (`clipAt`)
  ```js
  const f = fieldClipboard.entries[0];   // fieldClipboard = $state<{entries: MarcField[]}>(...)
  return f ? structuredClone(f) : undefined;
  ```
  `clipPush` stores a plain object, but reading it back through the module
  `$state` returns a proxy, so every paste path throws: `alt+v` in
  `MarcGrid.svelte:107`, `alt+v` in `MarcTextEditor.svelte:141`, and the
  `FieldClipboardPane` Paste buttons via `clipAt`.

The codebase already knows the idiom -- every other `structuredClone` call site
snapshots first:

```
screens/Macros.svelte:101       params = structuredClone($state.snapshot(m.params) ?? []);
components/MarcGrid.svelte:113  insertBelow(i, structuredClone($state.snapshot(f)));
```

These two call sites are the ones that forgot.

## Why it matters

Original cataloging -- typing a record from scratch -- is the single most
expensive thing a cataloger does in this application, and the screen for it does
not function. There is no error message that points at the truth; it says the
templates failed to load, and they did not.

The clipboard defect silently destroys work: `alt+x` is advertised as cut, so a
cataloger uses it to move a field, and the field simply disappears. Nothing tells
them the paste failed.

Both are invisible to server-side tests: the API is healthy in both cases.

## Expected

- `pick()` clones a snapshot: `structuredClone($state.snapshot(tpl.record))`, so
  the grid, the clipboard pane, and `Stage for review` render.
- `clipPeek()` / `clipAt()` snapshot before cloning (or `clipPush` stores frozen
  plain objects and the readers return a plain deep copy), so `alt+v` pastes and
  `alt+x` is recoverable.
- `loadTemplates()`'s catch should not report a load failure for an error thrown
  after the fetch succeeded.
- A regression test would catch either: mount `NewRecord` and assert the grid
  exists; `clipPush` then `clipPeek` and assert the field comes back.

## Repro

```
cd ~/libcat-e2e && node ui/probe_newrecord.mjs
```

Expect `N1` (the grid mounts), `N2` (the error text stops blaming the fetch), and
`N7`/`N8` (clipboard copy/paste and cut/paste) to flip to PASS; `N3`-`N6` (the
viability gate and staging) currently SKIP because they are blocked behind the
grid, and should start running once it mounts. The probe stages into a copycat
batch and deletes it; nothing is ever committed, so no work enters the catalog.
`harness/retest.mjs` carries the same check as `t224`.
