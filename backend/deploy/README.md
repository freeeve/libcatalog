# Deploying the Tier 2 backend

The backend is one `net/http` handler with two wrappers, so every deployment
shape serves identical routes. **The static tier needs none of this** -- the
graph is the contract, and a catalog built with `lcat` alone keeps working.

## Shapes

| Shape | Entry point | Datastore | Grain store |
|---|---|---|---|
| Laptop / dev | `go run ./cmd/lcatd` | in-memory | local dir (`LCATD_BLOB_DIR`) |
| Container (Cloud Run / Fargate / k8s / self-host) | `deploy/docker/Dockerfile` | DynamoDB (or DynamoDB-local) | S3 / R2 / MinIO |
| AWS serverless | `deploy/terraform/` (API GW v2 + Lambda) | DynamoDB | S3 |

## Local, no cloud at all

```sh
cd backend
LCATD_LISTEN_ADDR=:8471 \
LCATD_LOCAL_AUTH=1 \
LCATD_BOOTSTRAP_ADMIN=admin@example.org:changeme123 \
LCATD_ABUSE_SECRET=$(openssl rand -hex 16) \
LCATD_BLOB_DIR=/path/to/catalog-repo \
go run ./cmd/lcatd
```

Point `LCATD_BLOB_DIR` at a tree containing `data/works/` grains (any
`lcat ingest` output). Login, suggestion, moderation, publish, record
editing, and exports all work against the local directory; published edits
land as `editorial:` quads in the grain files, ready for
`lcat serialize && lcat project`.

## Read-only demo on Lambda (Function URL, ~$0)

A public **read-only** instance (patrons/catalogers explore but nothing
persists, `LCATD_READ_ONLY=1`) needs no DynamoDB or S3: the in-memory document
store plus a **bundled read-only grain dir** is enough, and each cold start
re-creates the bootstrap admin. `cmd/lcatd-lambda` builds the same handler as
`cmd/lcatd` (shared `backend/appdeps`) and serves the embedded SPA + API through
the Lambda adapter, so a **Function URL** (no API Gateway -> no per-request
charge) keeps it inside Lambda's always-free tier.

```sh
cd backend
# Build the SPA into the binary, then the arm64 Lambda bootstrap.
(cd ui && npm ci && npm run build)
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -o bootstrap ./cmd/lcatd-lambda
# Bundle the read-only grains next to the binary (LCATD_BLOB_DIR points at them).
zip -r lcatd-demo.zip bootstrap grains/
```

Deploy the zip as a `provided.al2023` (arm64) function with a Function URL and:
`LCATD_READ_ONLY=1`, `LCATD_BLOB_DIR=/var/task/grains`, `LCATD_LOCAL_AUTH=1`,
`LCATD_BOOTSTRAP_ADMIN=demo@example.org:<pw>`, `LCATD_LOCAL_SIGNING_KEY=<key>`
(a stable key so a session survives a warm instance), `LCATD_ABUSE_SECRET=<...>`.
Background workers are skipped in read-only mode, so the freeze-between-
invocations model is fine.

**A richer demo:**

- `LCATD_SANDBOX=1` (implies read-only) lets a visitor *edit* -- the record
  editor shows Save and renders each change as if committed, wiped on refresh --
  without anything persisting.
- **Subject search works out of the box:** the built-in `lcsh` source proxies to
  `id.loc.gov` live (`/v1/vocabsuggest`), so the picker autocompletes all of LCSH
  with no local load (the Lambda just needs outbound internet).
- **Existing subjects display** their real headings if you bundle a
  corpus-sized authority snapshot: `lcat vocab-subset --catalog catalog.json
  --out lcsh.nq`, drop it under `grains/data/authorities/vocab/lcsh.nq`, and set
  `LCATD_VOCAB_SCHEMES=lcsh` (a small file -> fast cold start). Reproject with it
  loaded to fill the public catalog's labels too.

**Turnkey terraform module** (`deploy/terraform/modules/readonly-demo/`): a
consumer supplies the built zip (via the module's `build-zip.sh`) and gets the
Lambda + Function URL + a CloudFront distribution wired with the right cache
split:

- `/assets/*` (hashed, immutable) -> cached hard at the edge, so a page renders
  without waking Lambda;
- `/config` and `/v1/*` -> never cached, forwarded to the function
  (all-viewer-except-Host, so auth/cookies pass but the Function URL's Host is
  preserved);
- HTML / SPA routes -> served fresh so a redeploy shows immediately.

So the cold start is only felt on the first API call. See the module's README to
wire it up (and a custom domain via an ACM cert in us-east-1). Caveat: concurrent
cold instances have separate in-memory stores, so token *refresh* can miss across
instances -- use a generous access-token TTL. The **writable** production stack
(below) is a separate deployment (tasks/099).

## Terraform (AWS reference)

```sh
cd backend
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -o bootstrap ./cmd/lcatd-lambda
zip lcatd-lambda.zip bootstrap
cd deploy/terraform
terraform init
terraform apply -var grain_bucket_name=my-catalog-grains -var lambda_zip=../../lcatd-lambda.zip
```

Creates: the pk/sk DynamoDB table (TTL + PITR), the versioned grain bucket
(exports auto-expire), the API lambda + HTTP API (CORS from
`allowed_origins`, throttle backstop, no gateway authorizer -- JWTs verify
in-function), SSM secret parameters to fill in after apply, and optionally
the EventBridge bus + SQS queue that carry grains-changed rebuild events.
Non-secret configuration (issuers, role maps, provider) goes in the
`environment` variable map.

## Docker / compose

`deploy/docker/compose.yaml` boots lcatd beside MinIO and DynamoDB-local --
the self-contained integration stack. The image is distroless static
(`deploy/docker/Dockerfile`, built from the repo root). The Dockerfile builds
the Svelte SPA in a `node` stage and embeds it before the Go build, so the image
serves a working browser UI.

**Building lcatd by hand:** the SPA is embedded via `go:embed backend/ui/dist`,
and the committed `dist/` is only a placeholder. A bare `go build ./cmd/lcatd`
therefore serves an API with a "UI not built" notice at `/` (and logs a warning
at startup). To embed the real app, build it first:

```sh
cd backend/ui && npm ci && npm run build
cd .. && go build ./cmd/lcatd
```

Kubernetes notes: the API is stateless (all state lives in the document
store and grain store), `GET /v1/healthz` serves liveness/readiness, SIGTERM
drains gracefully, and the advisory ingest lease already coordinates
single-flight work across replicas -- horizontal scaling is safe. The deeper
k8s ergonomics review (probe wiring, manifests vs Helm, the no-AWS worker
path, and the hugo-watcher local preview loop) is tracked as tasks/054.

## Rebuild pipeline

Publishes and enrichments change grains; something must re-run
`lcat serialize && lcat project` (and `lcat index` / Pagefind) and redeploy
the static site. Pick one:

- **Webhook** (`LCATD_WEBHOOK_URL`/`LCATD_WEBHOOK_SECRET`): lcatd POSTs a
  signed grains-changed event to any CI endpoint (verify with
  `trigger.Verify`).
- **Local command** (`LCATD_REBUILD_CMD`, optional `LCATD_REBUILD_DIR`): run
  a shell command after each publish (changed paths in
  `$LCAT_CHANGED_PATHS`). This is the hugo-watcher dev loop: point it at
  `lcat serialize && lcat project` targeting a running `hugo server`'s data
  directory and published edits live-reload in the discovery site within
  seconds -- no cloud, no CI. Composes with the webhook (both fire).
- **EventBridge/SQS** (Terraform `rebuild_events`): a worker consumes the
  queue and rebuilds.
- **Schedule**: rebuild on cron; skip event plumbing entirely.
