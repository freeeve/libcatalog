# 095 -- Wire the persistent stores (KV + blob) into the entrypoints

## Context

Everything the backend keeps in the document store (`store.Store`) is in-memory
and dies on restart: the audit trail (and the new editing-stats rollup that
reads it, `093`), the review queue/suggestions, folk terms, promotions, copycat
batches/targets/profiles, drafts, the seed marker, and rate-limit counters.
`cmd/lcatd/main.go:111` hardcodes `db := store.NewMem()` -- the comment there
says so: "The datastore is in-memory for now; the DynamoDB selection arrives
with the deployment task." `cmd/lcatd-lambda/main.go` is worse: it passes an
empty `httpapi.Deps{Logger: ...}`, so the Lambda serves no persistent state at
all.

What already survives a restart does so through the blob store, not the KV:
BIBFRAME grains (the catalog records) via the local blob dir, and installed
vocabularies via blob snapshots (`067`). Copycat targets only *appear* to
survive -- they are re-seeded every boot (`SeedDefaultTargets`, `main.go:197`)
because the seed marker lives in the ephemeral KV.

The persistent backends are already written and tested; this task is wiring, not
new subsystems:

- `backend/store/dynamo/dynamo.go` -- complete `store.Store` (Get/Put/Delete/
  Query/Increment), conformance-tested against DynamoDB Local.
- `backend/blobs3/s3.go` -- S3 `blob.Store`, tested.
- `backend/deploy/terraform/dynamodb.tf` -- the `${name}-sidecar` table: pk/sk,
  TTL on `expireAt`, PITR for the audit trail. IAM/Lambda scaffolding from `040`.

## Scope

- **Shared `buildDeps`.** Factor the dependency assembly out of
  `cmd/lcatd/main.go` so both `lcatd` and `lcatd-lambda` construct the same
  deps. Today only `lcatd` builds them and `lcatd-lambda` builds none.
- **Store selection.** In the shared builder, construct
  `dynamo.New(dynamodb.NewFromConfig(awsCfg), table)` when a table is
  configured; fall back to `store.NewMem()` for tests/local dev. Add config
  knobs to `backend/config/config.go` (e.g. `LCATD_DYNAMO_TABLE`, AWS region,
  optional `DYNAMO_ENDPOINT` for DynamoDB Local) alongside the existing
  `BlobDir`/`ListenAddr` env.
- **Blob selection.** Same treatment: construct `blobs3` (S3) when a bucket is
  configured, else `blob.NewDir(cfg.BlobDir)`. `lcatd` uses `NewDir` today and
  `lcatd-lambda` uses nothing.
- **Deploy glue.** Flow the terraform table name and S3 bucket into the Lambda
  environment; confirm the execution-role IAM grants the table read/write (TTL
  and PITR are already set in `dynamodb.tf`).
- **Seed idempotency under persistence.** `SeedDefaultTargets` runs every boot.
  Verify it is `CondIfAbsent` and race-safe across concurrent Lambda cold starts
  -- harmless re-seeding against mem must become a no-op against a shared table.

## Non-goals

- No new store or blob backend code (dynamo + s3 already exist).
- No data migration/backfill (KV state is greenfield/ephemeral today).
- The demo playground staying in-memory is acceptable; pointing it at DynamoDB
  Local so the audit trail survives restarts is optional follow-up, not required
  here.

## Acceptance

- `lcatd` with a configured table + bucket persists the audit trail (and copycat
  targets/batches, drafts, promotions) across a restart; `/v1/stats` for a past
  month still returns prior activity after a bounce.
- `lcatd-lambda` serves the full surface backed by dynamo + S3, not empty deps.
- With no table/bucket configured, both entrypoints fall back to mem/`NewDir`
  and existing tests pass unchanged.
- CI exercises the dynamo path: run the `store/dynamo` conformance test against
  DynamoDB Local (it currently `t.Skip`s unless `DYNAMO_ENDPOINT` is set).
- **Verify the TTL parity gap** before closing: DynamoDB deletes expired items
  lazily (documented at `dynamo.go:6`) and there is no read-time expired filter,
  whereas `store.NewMem` is strict. Confirm the `expireAt` callers (abuse
  rate-limiter via `Increment`, supporter TTLs) tolerate reading an expired
  record, or add a read-side filter -- note the decision in the task.
