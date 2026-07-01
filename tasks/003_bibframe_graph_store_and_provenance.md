# 003 — BIBFRAME graph store + provenance (N-Quads, RDFC-1.0)

Framework core. `docs/ARCHITECTURE.md` §3, §5; `docs/ROADMAP.md` Phase 1. The
canonical on-disk graph convention both tiers build on. (Consumer data
migration is `../qllpoc` `tasks/039`.)

## Goal

A canonical, diffable, provenance-tagged on-disk BIBFRAME graph.

## Decisions (per ARCHITECTURE §3/§5)

- **One format: N-Quads throughout.** Per-Work grains
  `data/works/<xx>/<workid>.nq` (sharded by id prefix so no dir holds 100k+
  entries); shared authorities under `data/authorities/<xx>/<id>.nq`, referenced
  by IRI. Bulk `catalog.nq` is the canonical-sorted concatenation of the grains
  — one libcodex writer, no conversion step. (Turtle can't carry named graphs;
  TriG would be an extra serializer to build.)
- **Provenance on the 4th column:** `feed:<provider>` (regenerated on ingest,
  never hand-edited) and `editorial:` (human/authority-owned, preserved) coexist
  in one per-Work-diffable file.
- **Deterministic labeling:** blank nodes canonicalized via **RDFC-1.0** (W3C RDF
  Dataset Canonicalization, née URDNA2015), skolemized to stable IRIs from those
  labels, statements sorted (graph column weighted early so feed/editorial lines
  occupy separate regions, shrinking merge-conflict surface). Re-serialize == a
  no-op diff. This determinism is a Phase-0 acceptance gate, not a detail.
- **Availability excluded** — live/volatile, fetched client-side, never
  committed.

## Work items

- N-Quads writer + RDFC-1.0 canonicalization in libcodex (the `bibframe`
  encoders emit triples today; the streaming decoder already reads N-Quads).
- Clobber-safe re-ingest as a within-file rewrite: drop the `feed:<provider>`
  lines, append the freshly generated feed lines, keep every `editorial:` line,
  re-canonicalize.

## Acceptance

- Re-serializing an unchanged Work is a zero-diff commit (RDFC-1.0 stable).
- A re-ingest round replaces `feed:*` and leaves `editorial:*` byte-identical.

## Refs

- libcodex `rdf` (canonicalization, N-Quads streaming), `bibframe`.
