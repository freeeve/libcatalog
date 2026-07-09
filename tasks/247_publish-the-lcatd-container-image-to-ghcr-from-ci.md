# 247 -- publish the lcatd container image to GHCR from CI

Opened 2026-07-09. Split out of tasks/054, which decided the repo owns the
`Dockerfile` but that registry publishing is a CI concern.

## Context

`Dockerfile` builds `lcatd` + `lcat` onto `distroless/static-debian12:nonroot`
(~40MB). It is built by hand today. `docs/deploy.md` already tells operators to
run `ghcr.io/freeeve/libcat:v0.97.0`, an image nobody publishes -- so that line
is currently a promise, not a fact.

## Scope

- A workflow that builds and pushes on a version tag, tagging the image with the
  release version and `latest`. Releases are lockstep across root/hugo/backend
  (`scripts/release.sh`), so the image version is the release version.
- Multi-arch (`linux/amd64`, `linux/arm64`) via buildx: the maintainer develops
  on arm64 and most clusters are amd64.
- `--build-arg VERSION` is already wired to `main.version`; pass the tag.
- Provenance/SBOM attestation if it is free with buildx; do not gold-plate.

## Non-goals

- A Helm chart, and any deployment automation. tasks/054 argued both out.

## Notes

- The `dynamo-conformance` workflow is currently **disabled** at the
  maintainer's request; re-enable before tasks/099's production deploy. Do not
  quietly re-enable it as part of this task.
- The build asserts `backend/ui/dist` is not the committed placeholder before it
  compiles, so a CI build that skips the Node stage fails loudly rather than
  shipping an empty admin UI. Keep that guard.

## Acceptance

- Pushing tag `vX.Y.Z` publishes `ghcr.io/freeeve/libcat:vX.Y.Z` and `:latest`,
  both arches, and `docker run ghcr.io/freeeve/libcat:vX.Y.Z` serves
  `/v1/healthz`.
- The image's `/admin/` is the real SPA, not the placeholder page.
- `docs/deploy.md`'s `ghcr.io/freeeve/libcat` reference resolves.
