# 036 -- Publisher, S3 blob store, rebuild triggers

## Context

Approved queue decisions and record edits become `editorial:` quads in grains
held in object storage. Concurrency discipline: conditional PUTs (ETag) with
bounded retry, plus an advisory ingest lease so feed re-ingest (single-flight,
non-concurrency-safe Resolver) and publishing interleave safely without ever
losing editorial statements.

## Scope

1. `backend/blobs3/s3.go`: `blob.Store` S3 impl (aws-sdk-go-v2; If-Match/
   If-None-Match conditional writes; custom endpoint + path-style for R2/MinIO;
   implements `blob.Signer` via presigned GETs).
2. `backend/publish/publisher.go`: drain approved-unpublished, group by grain,
   `Get -> ApplyEditorialPatch -> Put(IfMatch)` with jittered bounded retry;
   stamp publishedAt + resulting etag on queue items; audit entries.
3. `backend/publish/lease.go`: `LEASE#ingest` advisory lease (CondIfAbsent +
   TTL heartbeat); publisher defers (not drops) while held.
4. `backend/trigger/trigger.go`: `Notifier` interface, `noop`, `webhook`
   (HMAC-SHA256-signed POST); `trigger/awstrigger/` SQS + EventBridge.
5. Ingest adopts conditional writes: `cmd/lcat` ingest path via `LoadPriorStore`
   etags -- on conflict re-read grain, re-extract non-feed graphs, union, retry.

## Acceptance

- Race test on MemStore: concurrent editor Put during a simulated re-ingest
  loses neither the edit nor the feed rewrite.
- blobs3 conditional writes exercised against MinIO behind a build tag.
- Webhook signature verified by a test receiver; lease blocks a second ingest.
