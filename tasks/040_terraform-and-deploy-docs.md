# 040 -- Deployment: Terraform reference + container docs

## Context

qllpoc deploys its sidecar with Terraform; the generalized module ships an AWS
reference (Lambda + API GW v2 + DynamoDB + S3 + EventBridge) plus a documented
container path (Cloud Run/Fargate/self-host) for other clouds. Infra stays out
of core code.

## Scope

1. `backend/deploy/terraform/`: API GW v2 + lcatd-lambda + worker, DynamoDB
   single table (TTL, PITR), S3 grain bucket (versioning on, conditional
   writes), EventBridge/SQS trigger wiring, SSM/secrets params, CORS.
2. `backend/deploy/docker/`: Dockerfile + compose example (lcatd + MinIO +
   local DynamoDB) for self-host/dev.
3. Docs: ARCHITECTURE §6 updated from "described, not implemented" to the real
   design (module boundary, provenance write path, lease discipline); README
   quickstart for both deployment shapes; note the static tier runs unchanged
   with the backend absent.

## Acceptance

- `terraform validate` passes; compose stack boots lcatd healthy against
  MinIO + DynamoDB-local.
- Docs reviewed for the "graph is the contract" invariant.
