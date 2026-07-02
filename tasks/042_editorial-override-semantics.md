# 042 -- Editorial override semantics (`lcat:overrides`)

## Context

Editing a feed-sourced value must not fight the next re-ingest. The rule: the
editor writes the new value set as ordinary `editorial:` quads plus one marker
`<subject> lcat:overrides <predicateIRI>`; everywhere (projector, doc mapper,
MARC materializer), a marker shadows all `feed:*` quads for that (subject,
predicate). Replace/partial-remove/full-remove/revert all fall out; survives
re-ingest because the editorial graph is preserved verbatim.

## Scope

1. `bibframe/override.go`: write/read helpers for `lcat:overrides` markers
   (per-subject predicate sets), consistent with the merge.go marker style.
2. Projector honors shadowing: `feed:*` values for an overridden (subject,
   predicate) are ignored in catalog.json/facets.json.
3. Doc mapper (tasks/041) emits `prov` + `overridden` per field; revert = delete
   marker + editorial values (feed value resurfaces).

## Acceptance

- Override survives a simulated re-ingest (reingest_test.go pattern): feed
  rewrites, edited value wins in projection.
- Revert restores the feed value with zero residue.
- Partial removal (editorial set = feed minus one value) projects correctly.
