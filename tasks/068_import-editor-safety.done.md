# 068 -- Import and editor safety (Koha parity gaps)

## Context

A review of the Koha cataloging manual (2026-07) against our surfaces found
three workflow-safety gaps. The machinery exists: identity.Resolver powers
the copycat match banner, batches commit through the ingest pipeline, and
tasks/047 shipped the saved-query shape.

## Scope

1. **Pre-save duplicate warning**: on editor save -- and on the tasks/058
   create-work/clone path once it exists -- run identity.Resolver over the
   would-be doc; if it clusters with an existing work, show a non-blocking
   banner (open existing / continue anyway) mirroring copycat's match
   banner.
2. **Import batch undo**: `POST /v1/copycat/batches/{id}/revert`. Commit
   records the created/overlaid grain set; revert restores prior grain
   bytes and tombstones works the batch created. Grains whose post-commit
   state carries editorial edits are skipped and reported per-grain.
3. **Staging profiles**: saved import configurations `{targets, overlay
   policy, match choices}` on the /v1/queries saved-query shape,
   selectable at batch creation.

## Acceptance

- Saving an editor doc that clusters with an existing work warns without
  blocking; continue-anyway saves normally.
- Committing then reverting a batch restores prior grain bytes exactly
  (byte-stable), and a grain edited after commit survives with a reported
  skip.
- A saved staging profile pre-fills a new batch end to end.
