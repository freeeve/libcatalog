# 221 -- relationships-panel (058 item 3)

Opened 2026-07-09. Split from tasks/058 scope item 3.

## Outcome

Shipped in v0.71.0 (commit 976dbf1).

- **Work links**: bf:hasPart/bf:partOf stored in BOTH works' editorial
  graphs (the API writes the inverse on the target), so each grain
  self-describes and revert/re-ingest keep the links. GET lists with
  index-resolved titles; POST/DELETE pre-check both grains before
  writing either side, so a typo'd id never leaves a half-link.
  WORK_RELATE/WORK_UNRELATE audit. Editor grows a Relationships panel
  (immediate writes, like items/covers). Clones drop relation
  statements -- a carried link would be a half-link nothing
  reciprocates.
- **Series**: seriesStatement (490$a) + seriesEnumeration (490$v) as
  writable literal fields on the instance profile -- the ops machinery,
  drafts, and macros get them for free; no bespoke panel needed.

Verified live on the playground: linked the 217 demo clone partOf its
source -- both sides list each other with titles; series + enumeration
added via /ops and round-tripped through the doc.

Public surfacing (projector, hugo, MARC 490/77x) deliberately deferred
to tasks/222 -- it needs a catalog.json schema decision.
