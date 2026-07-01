# 002 -- Identity: minted-id persistence across re-ingest

## Problem
ARCHITECTURE.md §4 mints opaque Work/Instance ids **once**, "never derived from
a provider id." ARCHITECTURE.md §5 fully **regenerates** `feed:<provider>` on
every ingest. So each re-ingest must re-attach freshly generated feed statements
to the *previously minted* Instance/Work id -- which requires a persisted
`provider-id -> minted-id` map that ingest reads and writes. This mapping is
itself durable state; it is not currently specified.

## Scope
1. **Where the map lives.** Options: an `editorial:`-adjacent mapping graph
   committed to git (diffable, auditable) vs a sidecar index. Prefer the
   committed graph so identity is versioned with the data.
2. **Mint-or-resolve on ingest.** For each incoming provider record: resolve
   (ISBN / provider id) to an existing Instance, else mint a new id and record
   the mapping. Never re-mint for an id already mapped.
3. **Interaction with clustering.** Work-id assignment (`tasks/001`) reads the
   Instance map; merges/splits rewrite Work ids and must update the map
   atomically with the overlay.
4. **Collision + reassignment.** Handle a provider id that moves between
   Instances (rare, e.g. a provider re-issues an identifier) explicitly rather
   than silently.

## Acceptance
- Re-ingesting the same feed twice produces byte-identical grains (no id churn).
- Ingesting a changed feed preserves ids for unchanged records and mints only
  for genuinely new ones.
- The map round-trips through git with clean diffs (RDFC-1.0 canonical).
