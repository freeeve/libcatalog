# 001 -- Work clustering: correction path + URL stability

## Problem
Work clustering (ARCHITECTURE.md §4) relies on OpenLibrary work ids (uneven
coverage) with a computed author+title+language fallback. The computed key
**over-merges** (reissues, common titles, different works sharing an
access-point key) and **under-merges** (translations, transliteration/name
variants). Left uncorrected, mis-clusters degrade the core discovery unit, and
any re-cluster silently changes a Work's opaque id -- and therefore its public
URL.

## Scope
1. **Editorial merge/split overlay.** Represent human merge/split decisions as
   `editorial:` statements that the computed key cannot override on re-ingest.
   Design the predicates (e.g. `lcat:mergedInto`, `lcat:splitFrom`) and the
   precedence rule (editorial beats computed, always).
2. **Deterministic re-cluster.** Given the overlay + external ids + computed
   key, clustering must be reproducible: same inputs -> same Work assignment,
   with editorial decisions pinned.
3. **URL stability.** A merge/split must leave a redirect/tombstone so shared
   links and SEO survive. Decide where redirects live (emitted by the projector
   into the static build; a 301/410 map for the host) and how a tombstoned id
   resolves.

## Acceptance
- A curated set of known over/under-merge cases from qllpoc is corrected via the
  overlay and stays corrected across a full re-ingest.
- Re-clustering the corpus twice yields identical Work ids (determinism test).
- Every retired Work id resolves to its successor (redirect-map test).

## Notes
Ties into `tasks/002` (identity-map persistence): merges/splits mutate the
provider-id -> Work-id mapping and must update it atomically with the overlay.
