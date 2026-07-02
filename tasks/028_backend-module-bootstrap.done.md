# 028 -- Backend module bootstrap (`backend/`)

## Context

ARCHITECTURE §13 reserves `backend/` for the Tier 2 cataloging API. It becomes a
nested Go module (`github.com/freeeve/libcatalog/backend`, `replace => ../`) so
aws-sdk-go-v2/jwx/x/crypto never reach the core's 3-dependency tree -- the same
boundary pattern as the `hugo/` module. Core HTTP surface is a plain
`net/http` handler; compute adapters (container, Lambda) wrap it.

## Scope

1. `backend/go.mod` nested module (final nested-vs-single call happens here;
   default nested).
2. `backend/config/config.go`: env/file config (listen addr, blob store selection,
   datastore selection, issuers, vocab sources); no cloud SDKs.
3. `backend/httpapi/`: `func New(deps Deps) http.Handler` on stdlib `ServeMux`
   with method patterns; `GET /v1/healthz`; middleware scaffolding
   (request id, logging, panic recovery); `respond.go` JSON helpers.
4. `backend/cmd/lcatd/main.go`: single-binary server, graceful shutdown.
5. `backend/cmd/lcatd-lambda/main.go`: API Gateway v2 adapter wrapping the same
   handler (aws-lambda-go-api-proxy or algnhsa).

## Acceptance

- `go run ./cmd/lcatd` (in backend/) serves `/v1/healthz`.
- Lambda adapter compiles.
- Core module builds and tests green without the backend module present.
- CI-facing note in README/ARCHITECTURE about the two-module build.
