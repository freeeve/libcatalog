# 063 -- Keyboard triage for copy-cataloging review and duplicates merge

## Context

Phase 5 of the admin UX overhaul. CopyCat and Duplicates were the last mouse-only
screens: per-row radios/checkboxes in static tables, no bulk actions, several clicks per
record. Importing one record took search -> tick -> stage -> policy -> per-row radio ->
commit.

## Scope

- CopyCat split under 500 lines/file: `screens/CopyCat.svelte` (orchestrator, search,
  targets, batches + `.split` review pane, screenState), `components/CopycatResults.svelte`
  (scope `copycat`: j/k move, x/Space pick, a all/none, Enter stage picked),
  `components/CopycatReview.svelte` (scope `copycat-review` pushed while a batch is open:
  j/k, i import, s skip, A import-all-new, N skip-all-already, o open matched work,
  c commit behind a confirm Modal, Escape closes). Bulk A/N ship one
  `reviewCopycatBatch(id, {decisions})` call. Review rows are one line with tinted
  decision chips instead of radios.
- Duplicates: RowList over groups (scope `duplicates`, Enter/o expand), sub-scope
  `duplicates-compare` while open (1-9 pick survivor by column ordinal, m merge behind a
  confirm Modal listing the losers, Escape collapses); screenState keeps groups/selection
  across drill-ins.

## Acceptance

- Import flow via keyboard only: / query Enter, j/x picks, Enter stage, A, c, confirm.
- a11y suite green with updated fixtures; check/test/build green.
