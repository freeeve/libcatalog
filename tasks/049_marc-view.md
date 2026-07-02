# 049 -- MARC view (dual-view editor, fidelity sidecar)

## Context

Copy catalogers get a real MARC editing grid over the same grain: materialized
via libcodex `bibframe.Decode`, written back via `FromRecord` as a *diff* so
untouched fields are no-ops and MARC edits get identical override/audit
semantics. Crosswalk-lossy fields are preserved verbatim rather than silently
dropped.

## Scope

1. `bibframe/fidelity.go`: promote `knownLostFields`/`coreFields` out of
   roundtrip_test.go as the consumable loss table.
2. `lcat:marcVerbatim` sidecar: on MARC ingest, crosswalk-dropped fields stored
   verbatim as quads (stable tag+indicators+subfields serialization) in the
   record's graph; re-emitted on MARC export; visible read-only in the editor.
3. `backend/marcview/`: grain -> field-array JSON (`[{tag, ind1, ind2,
   subfields}]` + leader + verbatim + lossy annotations); edited array ->
   re-encode -> diff vs old encoding -> editorial ops.
4. SPA MARC tab: `MarcGrid.svelte` (keyboard-first: tab/enter navigation,
   duplicate-field key), `FixedFieldGrid.svelte` positional builders driven by
   JSON definitions (leader, 008 by material type, 006, 007), non-blocking
   lossy-tag warnings linking docs/marc-fidelity.md.

## Acceptance

- Open a vendored MARC Express record, edit one field, save: only that field's
  delta lands as editorial quads.
- Editing a lossy tag warns; the value round-trips to MARC export via
  lcat:marcVerbatim.
- Untouched-field MARC save is a no-op (grain byte-identical).
