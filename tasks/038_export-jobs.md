# 038 -- Export jobs (MARC/BIBFRAME to S3 with download links)

## Context

Catalogers export a selected subset or the full record set as MARC or BIBFRAME.
Runs as an async job writing to the blob store; the download is a time-limited
link (S3 presigned when the store implements `blob.Signer`, HMAC-token route
otherwise).

## Scope

1. `backend/export/export.go`: job model in the datastore (`JOB#EXPORT/<id>` +
   per-user index): requester, format, selection, status QUEUED|RUNNING|DONE|
   FAILED, outputPath, error, expiresAt.
2. Runner streams per-grain (never accumulates): `marc` via libcodex
   `bibframe.Decode` -> `iso2709.NewWriter`; `nquads` via the corpus
   re-serialization path scoped to the selection (grain concatenation is wrong
   -- blank scope); `jsonld` via libcodex emitters; `csv` of projected
   `project.Work` rows.
3. Routes: `POST /v1/exports` (librarian) -> 202 {jobId};
   `GET /v1/exports` / `GET /v1/exports/{id}` (own jobs; admin all) -> status +
   link; `GET /v1/exports/{id}/download?token=` fallback.
4. Execution: container -- worker loop (`cmd/lcatd-worker` or in-process);
   Lambda -- async self-invoke, in-request path under a size cutoff.
5. Expiry: `expiresAt` + bucket lifecycle note; TTL cleanup of job records.

## Acceptance

- MARC export of fixture grains re-parses via `bibframe.Decode`.
- Full-set N-Quads export equals `lcat serialize` output over the same grains.
- Presigned and token downloads both exercised; expired links rejected.
