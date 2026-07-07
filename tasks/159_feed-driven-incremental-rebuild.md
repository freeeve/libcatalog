# 159 -- Feed-driven incremental public rebuild + async trigger seam

Plane 2 propagation of [154]. Consumes the change feed ([156]) so a publish
regenerates only what changed, not the whole site. Depends on [156], [157],
[158].

## Scope

1. **Incremental propagation.** For each entry in the change feed since the last
   public build: regenerate that work's detail page (task 157) and update the
   RoaringRange index shards it touches (task 158). Deletes drop the page and
   remove the doc from its shard. `catalog.nq` / `catalog.json` regenerate for
   the touched works. No full-site render on a publish.
2. **Full rebuild only for seed / schema change.** A from-scratch build (initial
   seed, template or index-schema change that invalidates everything) is the
   only path that scans the whole corpus. That is the one place heavy compute is
   expected -- an ECS/Fargate batch, rare, not per publish.
3. **Trigger seam.** Evolve the existing `trigger.Fanout` /
   `trigger.Command` (`backend/appdeps/appdeps.go`, `cfg.RebuildCmd`) from a
   synchronous command into an async job dispatch (SQS -> ECS `RunTask`, or Step
   Functions) at scale. Small change to the trigger seam; no backend request-path
   logic change. Publishes coalesce/queue so a burst of edits batches into one
   incremental run.

## Out of scope

- The ECS task definition / infra deployment itself -- a consumer (queerbooks)
  concern; note the seam contract here.
- Admin-plane freshness -- that is [155]/[156]; the public plane is allowed to
  lag.

## Verify

- A single-work publish regenerates one detail page + its shard(s), not the
  corpus.
- A burst of publishes coalesces into one incremental run.
- A schema-change full rebuild reproduces the whole site + indexes.
