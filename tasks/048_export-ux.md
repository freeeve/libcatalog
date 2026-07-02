# 048 -- Export UX (selection, formats, exports screen)

## Context

Cataloger-facing half of export jobs (tasks/038): subset selection shares the
batch Selection model; formats are MARC .mrc, BIBFRAME N-Quads, JSON-LD, and
CSV of projected rows; the dialog is honest about MARC lossiness.

## Scope

1. `backend/exportsvc/` (or fold into export/): selection -> format emitters
   wiring over the 038 job runner; CSV columns from `project.Work` (+ item
   columns when items land).
2. SPA Exports screen: new-export dialog (format, selection summary, lossiness
   note linking the rendered marc-fidelity table), job list with status/record
   count/expiry, download links.
3. SelectionBar integration: "Export selection" from search results and batch
   screens.

## Acceptance

- Export of a search-selection produces exactly those works.
- MARC option shows the fidelity note; `lcat:marcVerbatim` re-emission noted
  once 049 lands.
- Job list reflects live status transitions; expired jobs show as expired.
