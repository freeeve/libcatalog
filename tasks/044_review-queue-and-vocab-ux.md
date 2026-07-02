# 044 -- Review queue + vocabulary/tag UX

## Context

The staff moderation experience and the subject/tag input surfaces: qllpoc's
QueueScreen generalized to multi-vocab and multi-item-type, the vocab picker
with SKOS neighborhood browsing, folksonomy tag entry with typeahead, and the
tag-promotion flow. With 043 and 045 this is the MVP bar: usable subject/tag
review + a basic Work editor.

## Scope

1. Queue screen: filter by status/scheme/provenance/type; approve / reject /
   substitute (opens picker) / tombstone / dispute-context; publish bar gated
   to librarian; folk-term accept/block.
2. `VocabPicker.svelte` + `NeighborhoodPanel.svelte`: source tabs from config,
   search-as-you-type via `GET /v1/terms`, term card (definition/scope note),
   broader/narrower/related walk, multi-select chips, full keyboard operation.
3. `TagInput.svelte`: suggest-as-you-type from existing tags with counts;
   novel-term submission into the folk PROPOSED lifecycle.
4. Promotion path: tag chip -> "Promote..." -> picker -> `PROMOTION` queue item;
   approval batch-rewrites tag -> `bf:subject <termURI>` across carrying Works
   and records `lcat:tagAlias` in the authority graph.

## Acceptance

- End-to-end on MemStore/DirStore: anonymous suggestion -> moderator approve ->
  librarian publish -> `bf:subject` quad in the grain.
- Promotion rewrites all carrying Works (batch op) and future entries of the
  tag auto-suggest the controlled term.
- Keyboard-only operation of picker and queue verified.
