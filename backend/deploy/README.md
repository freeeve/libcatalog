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
(`deploy/docker/Dockerfile`, built from the repo root).

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
- **EventBridge/SQS** (Terraform `rebuild_events`): a worker consumes the
  queue and rebuilds.
- **Schedule**: rebuild on cron; skip event plumbing entirely.
