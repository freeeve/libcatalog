# Deploying lcatd as a container

`lcatd` is a plain `net/http` server. The Lambda entrypoint
(`backend/cmd/lcatd-lambda`) wraps the same handler, so the container shape is a
peer of the Lambda shape rather than a dev convenience: same routes, same
configuration, same probes.

For the AWS Lambda + API Gateway reference stack, see
`backend/deploy/terraform/` (tasks/040). For the no-cloud local loop, see
[local-dev.md](local-dev.md).

## The image

```sh
docker build -t libcat:dev --build-arg VERSION=v0.97.0 .
```

Three stages: Node builds the cataloging SPA, Go builds `lcatd` and `lcat`
statically, and the runtime is `gcr.io/distroless/static-debian12:nonroot` --
about 40MB, no shell, no package manager, running as uid 65532 with
`ca-certificates` present for OIDC discovery, S3, and vocabulary downloads.

Both binaries ship: `lcatd` is the entrypoint, and `lcat` is the projector CLI
that `LCATD_REBUILD_CMD` shells out to (`lcat serialize && lcat project`).

The SPA is `go:embed`ed into `lcatd`, and the `backend/ui/dist` committed to git
is a **placeholder**. The build asserts that the placeholder is gone before it
compiles, because a binary carrying the placeholder starts cleanly, serves an
empty page, and passes every server-side test.

```sh
docker run --rm -p 8080:8080 \
  -e LCATD_LOCAL_AUTH=1 \
  -e LCATD_BOOTSTRAP_ADMIN=you@example.org:changeme123 \
  -e LCATD_ABUSE_SECRET=$(openssl rand -hex 16) \
  -e LCATD_BLOB_DIR=/data \
  -v "$PWD/site:/data" \
  libcat:dev
```

The image declares no `HEALTHCHECK`: distroless has no shell to run one.
Orchestrators probe over HTTP, which needs nothing inside the container.

## Health probes

Two probes, two different questions. Wiring them to the same thing defeats both.

| Probe | Endpoint | Wire it to | Never wire it to |
|---|---|---|---|
| Liveness | `GET /v1/healthz` | the process | any datastore |
| Readiness | `GET /v1/readyz` | traffic admission | any shared dependency |

`healthz` reports on the process and nothing else, and keeps returning `200`
while the server drains -- a draining server is working as intended, and
restarting it would kill the in-flight requests the drain exists to protect. A
liveness probe that reaches a datastore turns a dependency blip into a restart
storm.

`readyz` returns `200` normally and `503` once the process has received
`SIGTERM`. It does **not** check store connectivity, though it easily could:
every replica shares one store, so a store blip would fail every replica's
readiness at once and the orchestrator would empty the Service of endpoints,
turning a degradation that still serves cached reads into a total outage. A
probe whose failure mode is "remove all capacity" must not depend on anything
shared.

There is no startup probe to configure. `appdeps.Build` -- which loads the
vocabularies, a few seconds of work -- completes before the listener opens, so
the first successful TCP connection already means the replica is warm.

## Shutdown, and the deregistration race

Kubernetes removes a terminating pod from its Service endpoints *concurrently*
with sending `SIGTERM`, not before it. A server that stops listening the moment
it is signalled refuses the requests a load balancer is still routing to it for
the width of that race.

Set `LCATD_SHUTDOWN_DELAY` to comfortably more than one readiness period.
`lcatd` then fails readiness immediately on `SIGTERM`, keeps serving for the
delay while it is deregistered, and only then drains in-flight requests (10s
grace). The default is `0` -- drain at once -- which is what a local run wants.

Give the pod a `terminationGracePeriodSeconds` larger than
`LCATD_SHUTDOWN_DELAY` + 10s, or the kubelet will `SIGKILL` mid-drain.

## Kubernetes

The API keeps no request state: it lives in the blob store and the document
store, and the ingest lease provides single-flight coordination for the
pipeline. Replicas therefore scale horizontally **once every replica shares the
same token-signing key** -- see below.

### Scaling past one replica needs a shared signing key

With `LCATD_LOCAL_AUTH=1` and no `LCATD_LOCAL_SIGNING_KEY`, each process mints
an **ephemeral Ed25519 key at boot**. Two replicas then sign tokens the other
rejects: behind a round-robin load balancer a cataloger's session fails with
`401` on roughly half of its requests, and the failure looks intermittent rather
than configured.

Generate one key, mount it as a Secret, give it to every replica:

```sh
openssl rand -base64 32     # a 32-byte Ed25519 seed; raw-url base64 also accepted
```

```yaml
- name: LCATD_LOCAL_SIGNING_KEY
  valueFrom:
    secretKeyRef: { name: lcatd-secrets, key: signing-key }
```

Verified against two replicas over one MinIO and one DynamoDB Local: without the
shared key a token minted by A is `401` at B; with it, A's token is accepted at
B and a draft written on A is read back on B.

A single replica needs none of this -- the ephemeral key is fine, and only
restarts invalidate sessions. With external SSO (`LCATD_OIDC_ISSUER`) tokens are
signed by the issuer, so the question does not arise.

```yaml
spec:
  terminationGracePeriodSeconds: 30   # > LCATD_SHUTDOWN_DELAY + 10s drain
  containers:
    - name: lcatd
      # Not published yet (tasks/247) -- build and push to your own registry.
      image: ghcr.io/freeeve/libcat:v0.98.0
      ports: [{ containerPort: 8080 }]
      env:
        - name: LCATD_SHUTDOWN_DELAY
          value: "5s"
      envFrom:
        # LCATD_ABUSE_SECRET, LCATD_LOCAL_SIGNING_KEY, OIDC client secret.
        # The signing key must be identical across replicas -- see above.
        - secretRef: { name: lcatd-secrets }
        - configMapRef: { name: lcatd-config }
      livenessProbe:
        httpGet: { path: /v1/healthz, port: 8080 }
        periodSeconds: 10
      readinessProbe:
        httpGet: { path: /v1/readyz, port: 8080 }
        periodSeconds: 2      # small, so the drain window can be small
      securityContext:
        runAsNonRoot: true
        readOnlyRootFilesystem: true
        allowPrivilegeEscalation: false
        capabilities: { drop: ["ALL"] }
```

`readOnlyRootFilesystem: true` works as-is when the grain store is S3
(`LCATD_S3_BUCKET`). With `LCATD_BLOB_DIR`, mount a writable volume there.

Configuration is environment-only, which is what `envFrom` consumes directly, so
secrets arrive as a `secretRef` and need no file mounts.

## Storage

| Env | Selects |
|---|---|
| `LCATD_BLOB_DIR` | a local directory grain store |
| `LCATD_S3_BUCKET` | an S3-compatible grain store (takes precedence) |
| `LCATD_DYNAMO_TABLE` | the document store |
| `LCATD_AWS_ENDPOINT` | redirects both S3 and DynamoDB clients |

`LCATD_AWS_ENDPOINT` is the seam for running off AWS: it points the S3 and
DynamoDB clients at any compatible endpoint (MinIO, ScyllaDB Alternator). The
S3 client already uses path-style addressing, which MinIO requires.
