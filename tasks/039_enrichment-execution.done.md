# 039 -- Enrichment execution (subject import, approval gate or auto-approve)

## Context

`ingest.RoleEnrich` is defined but never executed ("enrichment execution is
future work"). This task builds that path: enrichment providers yield candidate
controlled subjects per Work; per-source config routes them to the moderation
queue (approval gate) or directly into the graph (auto-approve).

## Scope

1. `ingest/enrich.go` (core, stdlib-only): `Enricher` interface
   (`Name()`, `Enrich(ctx, []WorkSummary) ([]Enrichment, error)`);
   `Enrichment{WorkID, Subjects []bibframe.AuthoritySubject, Extras}`;
   `WorkSummary` derived from grains (id, title, contributors, identifiers).
   `RunEnrich` direct mode: `ReplaceGraph(grain, EnrichmentGraph(name), quads)`
   via conditional writes -- idempotent re-enrich.
2. Backend wiring: per-source config `mode: queue|direct`; queue mode converts
   Enrichments to suggestions with `Provenance: PIPELINE` + confidence;
   `POST /v1/enrich/{source}/run` (admin) + worker/CLI execution.
3. Reference network enricher: id.loc.gov LCSH suggest API in `ingest/locsh/`
   behind the same Registry factory shape. (Hardcover's ingest-time
   SubjectEnricher stays as-is.)

## Acceptance

- Direct mode drop-and-replaces `enrichment:<name>` idempotently; re-ingest
  preserves the graph.
- Queue mode lands PIPELINE suggestions reviewable in `/v1/queue`.
- locsh enricher tested against recorded fixtures (no live network in tests).
