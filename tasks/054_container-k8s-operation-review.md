# 054 -- Review: container/k8s-style operation for the backend

## Context

The backend is deliberately Lambda-optional: `backend/cmd/lcatd` is a plain
net/http server (the Lambda command wraps the same handler), so it already
runs locally for testing with the in-memory datastore and a DirStore grain
tree -- no AWS. tasks/040 covers a docker-compose dev stack (lcatd + MinIO +
DynamoDB-local) and container *deployment docs*. This task is the fuller
review the maintainer asked for: whether and how to support first-class
container/k8s operation as a peer of the Lambda shape, not just a dev
convenience.

## Scope (review first, then implement what's applicable)

1. **Local-dev ergonomics now**: document the no-cloud loop (`LCATD_LOCAL_AUTH
   =1 LCATD_ABUSE_SECRET=... LCATD_BLOB_DIR=... go run ./cmd/lcatd`); decide
   whether a `lcatd --dev` flag should bundle sensible defaults (ephemeral
   key, mem store, bootstrap admin) into one switch.
2. **Container image**: multi-stage Dockerfile (static binary, distroless or
   alpine), image for lcatd + lcatd-worker; publish story (GHCR?).
3. **Compose dev stack** (overlaps 040 -- decide which task owns it): lcatd +
   MinIO + DynamoDB-local + a rebuild-webhook receiver stub; seed script for
   a fixture catalog.
4. **k8s readiness**: the API is stateless (all state in blob store +
   document store) and the ingest lease already provides single-flight
   coordination, so horizontal scaling should be safe -- verify. Add
   /v1/healthz-based liveness/readiness probes (readiness could check store
   connectivity), graceful-shutdown behavior under SIGTERM (exists), and
   decide manifests vs Helm chart vs "document kustomize examples only."
5. **Worker model outside Lambda**: lcatd-worker (or in-process goroutines)
   consuming publish/export/enrich work -- define how the SQS trigger maps to
   a container-native alternative (webhook receiver or store-polling loop) so
   k8s deployments need no AWS messaging.
6. **Secrets/config**: env-only today; review k8s secret mounting and
   whether file-based config (LCATD_CONFIG_FILE) is worth adding.
7. **Hugo-watcher dev loop (maintainer idea)**: exploit `hugo server`'s file
   watcher as the local preview of the whole editorial cycle. Shape: a local
   `trigger.Notifier` (e.g. `trigger.Command` or a small `lcatd --dev`
   built-in) that, on grains-changed, runs `lcat serialize && lcat project`
   into the running Hugo site's data dir -- the watcher sees catalog.json
   change and live-reloads, so an edit published in the cataloging UI appears
   in the discovery site within seconds, no cloud, no CI. Assessment so far:
   -- rebuild loop: YES, cheap and high-value; it is just a Notifier impl
      plus docs (hugo already watches the data dir).
   -- hosting the admin/review SPA *through* hugo: workable for integration
      demos (drop the built dist/ under static/admin/), but day-to-day SPA
      dev wants Vite's dev server (HMR) and production wants lcatd's
      go:embed; recommend documenting all three rather than making hugo the
      SPA host.
   Scope the Notifier impl + a `docs/local-dev.md` walkthrough here.
8. **S3 Tables (assessed 2026-07, maintainer question): not for current
   stores; candidate for a future analytics tier.** S3 Tables is managed
   Iceberg -- columnar/batch, engine-queried, seconds latency, no per-item
   conditional ops. The sidecar needs ms point reads + CAS (queue
   transitions, lease, refresh rotation): DynamoDB-class. Grains are
   path-addressed RDF documents needing ETag CAS and human/git readability:
   plain-S3-class. Revisit if/when these arrive: audit-trail analytics at
   consortium scale, availability-history time series (deeplibby-shaped,
   hundreds of millions of rows), or Parquet as an export format for
   data-science consumers.
9. **Decide scope**: which pieces are core-repo deliverables vs deployment-
   repo examples, and fold the result into tasks/040 or supersede it.

## Acceptance

- Written recommendation (in this task file or docs/) with the decided scope.
- Whatever is deemed applicable implemented: at minimum the Dockerfile,
  compose stack, probes, and the no-AWS worker path have concrete homes
  (here or 040).

## Recommendation

**Container operation is a peer of the Lambda shape, and the core repo owns the
image, the compose stack, and the probes. It does not own a Helm chart, a
published registry image, or a second config format.** The reasoning, item by
item, and what shipped.

Everything below was verified by running it, not by reading it. Where a claim
could not be verified it is marked as such.

### 1. Local-dev ergonomics -- **no `--dev` flag**

Documented instead: `docs/local-dev.md`. A flag bundling "ephemeral key, mem
store, bootstrap admin" would be a second source of truth that only the local
path exercises, while the thing most worth exercising locally is the
environment-variable configuration production actually uses (and that k8s
`envFrom` consumes directly). The one-liner is five variables.

### 2. Container image -- **shipped**; registry publishing **split out**

`Dockerfile`: Node builds the SPA, Go builds `lcatd` + `lcat` statically,
runtime is `distroless/static-debian12:nonroot`. 39.9MB, no shell, uid 65532,
`ca-certificates` present. `lcat` ships alongside `lcatd` because
`LCATD_REBUILD_CMD` shells out to it.

The build **asserts `backend/ui/dist` is not the committed placeholder** before
compiling. A binary carrying the placeholder starts cleanly, serves an empty
page, and passes every server-side test -- exactly the bug that survives CI. The
guard was checked by running its expression against the real placeholder.

GHCR publishing is a CI concern, not a repo-shape concern -> **tasks/247**.

### 3. Compose dev stack -- **shipped**, and it found a real gap

`compose.yaml`: lcatd + MinIO + DynamoDB-local + two init jobs (bucket, pk/sk
table matching `backend/deploy/terraform/dynamodb.tf`).

This could not be expressed before now. `LCATD_AWS_ENDPOINT` overrides the
endpoint for *every* AWS client at once, which suits one compatible endpoint
(LocalStack) but cannot address the off-AWS shape of two unrelated servers --
MinIO for blobs, DynamoDB Local or ScyllaDB Alternator for documents. Added
`LCATD_S3_ENDPOINT` and `LCATD_DYNAMO_ENDPOINT`, per-service, falling back to
`LCATD_AWS_ENDPOINT`. **This is a prerequisite for tasks/165** (ethical hosting),
whose premise was that the existing seam already sufficed. It did not.

Verified: lcatd selects `backend=s3` and `backend=dynamodb`, writes
`data/workindex.snapshot` into MinIO, serves `GET /v1/works` 200 off it, and
round-trips a draft into DynamoDB Local (scan shows the items).

tasks/040 is `done` and owned the Terraform reference plus container *docs*;
this task supersedes its container-docs portion with `docs/deploy.md`. No
merge needed.

### 4. k8s readiness -- **probes shipped; horizontal scaling verified, with a precondition**

`GET /v1/readyz` added. `GET /v1/healthz` stays liveness.

The task suggested "readiness could check store connectivity". **It should not.**
Every replica shares one store, so a store blip fails every replica's readiness
at once and the orchestrator empties the Service of endpoints -- converting a
degradation that still serves cached reads into a total outage. A probe whose
failure mode is "remove all capacity" must not depend on anything shared.
Liveness must not touch a datastore either, for the symmetric reason: it
restarts the container.

Readiness earns its keep at **shutdown**. k8s deregisters a terminating pod
concurrently with `SIGTERM`, so a server that stops listening on the signal
refuses requests still being routed to it. `LCATD_SHUTDOWN_DELAY` (default `0`)
now fails readiness immediately, keeps serving through the deregistration
window, then drains. Verified end to end: `readyz` 503 while `healthz` 200 and
requests still served for the full window, then exit 0 under `docker stop`.

No startup probe is needed: `appdeps.Build` (vocabularies, seconds) completes
before the listener opens, so the first successful connection means warm.

**Horizontal scaling holds only with a shared `LCATD_LOCAL_SIGNING_KEY`.**
Measured on two replicas over one MinIO and one DynamoDB Local: without it, a
token minted by A is `401` at B -- behind a round-robin LB a session fails on
roughly half its requests, looking intermittent rather than misconfigured. With
it, A's token is accepted at B and a draft written on A is read back on B. The
code already warns at boot and hard-errors under Lambda; the gap was that the
deployment docs never said it. Now in `docs/deploy.md`.

**Manifests, not Helm.** `docs/deploy.md` carries a Pod spec with both probes,
`terminationGracePeriodSeconds`, and a hardened `securityContext`. A chart in
the core repo would version-skew against a deployment repo that must own its own
values; a copyable manifest does not.

### 5. Worker model outside Lambda -- **no `lcatd-worker` binary**

The container shape needs none. `trigger.Notifier` already has two non-AWS
implementations -- `trigger.Command` (run a rebuild) and `trigger.Webhook`
(HMAC-signed POST) -- fanned out and debounced by `trigger.Coalesce`. A
long-lived container runs the publish/export drains in process; SQS and
EventBridge are the Lambda shape's answer to not having a process. Adding a
second binary would create a deployment topology nobody needs.

*Not verified*: the export/publish drains under a container over hours. The
seam is right; the soak test is not this task.

### 6. Secrets/config -- **env-only, no `LCATD_CONFIG_FILE`**

k8s `envFrom` consumes a Secret and a ConfigMap directly into the environment,
which is exactly the surface `config.FromEnv` reads. A file format would add a
parser, a precedence order, and a second thing to keep in sync with
`docs/api.md`, in exchange for nothing k8s cannot already do.

### 7. Hugo-watcher dev loop -- **documented; the code already existed**

`trigger.Command` already implements this and cites this task by name. What was
missing was the walkthrough: `docs/local-dev.md` wires `hugo server`'s data-dir
watcher to `LCATD_REBUILD_CMD`, and documents all three ways to serve the SPA
(Vite for HMR, `go:embed` for demos, the image for production) rather than
making hugo the SPA host.

Writing that walkthrough surfaced a genuine bug: **`lcat project` with a
`--provider` matching no feed writes an empty `catalog.json` and exits 0**, which
in a rebuild loop silently empties the public site. Filed as **tasks/246**.

### 8. S3 Tables -- **unchanged**

The prior assessment stands. Not for the current stores; revisit for an
analytics tier.

### 9. Scope

| Piece | Home |
|---|---|
| Dockerfile, `.dockerignore` | core repo (shipped) |
| `compose.yaml` | core repo (shipped) |
| `/v1/readyz`, drain sequencing | core repo (shipped) |
| Per-service AWS endpoints | core repo (shipped) |
| `docs/deploy.md`, `docs/local-dev.md` | core repo (shipped) |
| Registry publishing (GHCR) | CI -> tasks/247 |
| Helm chart | out of scope, deliberately |
| `LCATD_CONFIG_FILE` | rejected |
| `lcatd-worker` | rejected |

## Outcome

Shipped in **v0.98.0**.

- `af0c098` -- `/v1/readyz`, `Health`, `LCATD_SHUTDOWN_DELAY`, drain sequencing.
- `4d0ef1b` -- `Dockerfile`, `.dockerignore`, `compose.yaml`,
  `LCATD_S3_ENDPOINT` / `LCATD_DYNAMO_ENDPOINT`, `docs/deploy.md`,
  `docs/local-dev.md`.

Filed while doing it: **tasks/246** (silent empty projection -- a real bug found
in `lcat project`), **tasks/247** (GHCR publishing).

Left undone on purpose: Helm chart, `LCATD_CONFIG_FILE`, `lcatd-worker`,
`--dev`. Each is argued above rather than merely skipped.
