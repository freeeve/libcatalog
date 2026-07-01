# 006 — Provider interface + MARC import

Framework core. `docs/ARCHITECTURE.md` §6, §9; `docs/ROADMAP.md` Phase 3.
Pluggable ingest + the Tier-1 onboarding ramp ("bring your ILS's MARC").

## Goal

A provider interface; OverDrive as the reference provider; MARC import (via
libcodex) as a first-class provider.

## Approach

- `ingest/`: a provider maps its feed → `bf:Instance` triples under the
  `feed:<provider>` named graph; new items attach to an Instance by ISBN and to a
  Work by the task-002 clustering rules.
- **MARC provider:** libcodex reads `.mrc`/MARCXML/MODS/UNIMARC/BIBFRAME →
  `codex.Record` → reverse crosswalk (task 001) → `bf:Instance`. SRU fetch is
  plain HTTP returning MARCXML — point `marcxml.NewReader` at the response body.
- **OverDrive provider:** the qllpoc consumer contributes its `internal/libby`
  (thunder crawl) as the reference provider (see `../qllpoc` `tasks/040`).

## Acceptance

- A sample ILS MARC export (e.g. from Koha) imports to a valid Work/Instance
  graph and renders through Tier 1.
- SRU → MARCXML → import works end-to-end for one source.

## Refs

- libcodex readers: `iso2709`, `marcxml`, `mods`, `unimarc`, `bibframe`.
- Extraction reference: qllpoc `internal/libby/*`, `lambda/ingest`.
