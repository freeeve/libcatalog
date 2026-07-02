# 035 -- Editorial patch helpers (core `bibframe`)

## Context

`AddMergeMarker`/`AddSplitMarkers` embody the safe editorial write pattern
(parse grain -> add quads in `editorial:` -> canonicalize -> write). The dynamic
module needs the general form, plus graph constructors for the two remaining
provenance classes and a blob.Store-aware `LoadPrior`.

## Scope

1. `bibframe/editorial.go`: `Patch{Add, Remove []rdf.Quad}` and
   `ApplyEditorialPatch(grainNQ, Patch) ([]byte, error)` -- targets
   `EditorialGraph()`, IRI-only (blank nodes rejected per the graphcorpus
   constraint), idempotent adds, canonicalizes. `ReplaceGraph(grainNQ, g, quads)`
   (drop-and-replace one named graph). `AuthorityGraph(vocab)`,
   `EnrichmentGraph(name)` constructors.
2. Rewrite `AddMergeMarker`/`AddSplitMarkers` as thin wrappers (behavior
   preserved; existing tests must pass unchanged).
3. `AppendAuthoritySubject(grainNQ, workID, AuthoritySubject, graph)` for
   suggestion/enrichment publishes.
4. `bibframe/reingest_store.go`: `LoadPriorStore(ctx, blob.Store, provider)`
   returning `Prior` plus per-grain etags.

## Acceptance

- Existing merge/split/reingest tests pass unchanged.
- Blank-node patches rejected with a clear error.
- `LoadPriorStore` over MemStore matches `LoadPrior` over the same tree.
- Reingest preserves `enrichment:<name>` and `authority:<vocab>` graphs
  (preservedQuads already keeps non-feed graphs -- add the explicit test).
