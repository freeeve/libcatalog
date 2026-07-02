# 047 -- Batch operations, macros, command palette

## Context

One op-list machinery serves Koha's batch record modification, MARC
modification templates, and advanced-editor macros. The shared Selection model
also feeds exports (tasks/048).

## Scope

1. `backend/batch/`: `Selection = {kind: search|savedQuery|ids|importBatch|all}`
   + resolver; batch executor (per-grain apply with per-op results, dry-run
   mode returning aggregate diff); saved queries in the datastore.
2. Macros: `{id, label, keys?, ops[], params[]}` recorded in the editor
   (capture ops as you edit), replayed against the current doc, stored per-user
   + library-shared; parameter prompts; keyboard-assignable. Shared macros run
   in batch context = MARC modification templates.
3. SPA: BatchOps screen (selection bar on search results, dry-run diff,
   execute, results), Macros manage/record/replay screen, CommandPalette
   (Ctrl+K: fuzzy actions, run macro, jump to work).

## Acceptance

- Batch dry-run shows exact quad deltas; execute applies with per-record
  success/failure reporting and audit entries.
- A recorded macro replays on another record; a shared macro runs over a
  selection.
