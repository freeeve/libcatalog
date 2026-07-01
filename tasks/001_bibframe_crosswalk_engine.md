# 001 — Record ↔ BIBFRAME crosswalk engine

Framework core. See `docs/ARCHITECTURE.md` §2–4 and `docs/ROADMAP.md` Phase 0.
The keystone: one crosswalk unlocks export, half of import, and the graph the
whole system builds on.

## Goal

A generic `codex.Record` ↔ `bf:Work`/`bf:Instance` crosswalk over libcodex,
serialized as canonical N-Quads (per-Work grains + bulk `catalog.nq`; see task
003).

## Approach

- `bibframe/` package: build MARC-shaped `codex.Record`s from a normalized input
  record, then map to `bf:Work` (content) + `bf:Instance` (edition/format),
  linked by `bf:instanceOf` / `bf:hasInstance`.
  - 245/250; 100/700 creators with relators; 020 ISBNs; 264; 490/830; 520; 650
    $2 subject URIs; 008 fixed-field; 776/775 sibling-edition links.
- Reverse direction (BIBFRAME → `codex.Record`) underpins MARC export and MARC
  import (task 006).
- Serialization / canonicalization / provenance live in task 003 (N-Quads,
  RDFC-1.0).

## Acceptance

- Round-trips: `bibframe.Decode(Encode(rec))` stable on a fixture set.
- Emits valid MARCXML/MODS/schema.org from the same crosswalk (libcodex
  `Validate`).
- MARC ↔ BIBFRAME is lossy in both directions — ship a documented **known-loss
  list** and measure round-trip fidelity; do not assume it.
- Validated on a real corpus: qllpoc's ~6,266 records; field mapping is
  `../qllpoc` `tasks/038`.

## Refs

- libcodex `../libcodex`: `bibframe`, `mods`, `schemaorg`, `marcxml`, `rdf`.
- Extraction reference: qllpoc `internal/hugogen/generate.go`.
