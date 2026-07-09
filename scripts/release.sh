#!/usr/bin/env bash
# release.sh vX.Y.Z -- tag and push a lockstep release (tasks/146).
#
# The repo ships three Go modules (root, hugo/, backend/) whose tag families
# drifted after v0.7.2, leaving consumers to maintain pairing tables by hand.
# One release = one number everywhere: this script verifies the tree, runs
# the Go suites and the exampleSite build (the schema-pin compatibility
# gate), then releases in two phases (tasks/185): root and hugo tag at
# HEAD and push first, because backend/go.mod carries no replace directive
# (it would break `go install pkg@version` for adopters; in-repo dev rides
# an untracked go.work instead) and so backend's go.sum needs the real
# hash of the published root module -- which only exists once the v<V> tag
# is fetchable. Phase two points backend at the fresh root release,
# commits, tags backend/v<V> one commit later, and pushes.
#
# SKIP_TESTS=1 skips the test gate (rerelease of an already-green commit).
set -euo pipefail

cd "$(dirname "$0")/.."

V="${1:-}"
V="${V#v}"
if ! [[ "$V" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  echo "usage: scripts/release.sh vX.Y.Z" >&2
  exit 1
fi

branch=$(git rev-parse --abbrev-ref HEAD)
if [[ "$branch" != "main" ]]; then
  echo "release.sh: on branch $branch, releases cut from main" >&2
  exit 1
fi
# Tracked changes only: cross-session task files under tasks/ are left
# untracked by convention (the filing session leaves them; the owning side
# adopts) and must not block a release.
if [[ -n "$(git status --porcelain -uno)" ]]; then
  echo "release.sh: working tree has uncommitted tracked changes" >&2
  exit 1
fi

# The new version must be strictly newer than every existing tag in every
# family -- pre-lockstep numbers (pushed or local-only) stay burned.
for fam in "" "hugo/" "backend/"; do
  newest=$(git tag -l "${fam}v*" | sed "s|^${fam}v||" | sort -V | tail -1)
  if [[ -n "$newest" ]] && [[ "$(printf '%s\n%s\n' "$newest" "$V" | sort -V | tail -1)" != "$V" || "$newest" == "$V" ]]; then
    echo "release.sh: ${fam}v$V is not newer than existing ${fam}v$newest" >&2
    exit 1
  fi
done

if [[ "${SKIP_TESTS:-}" != "1" ]]; then
  echo "==> go test (root)"
  go test ./...
  echo "==> go test (backend)"
  (cd backend && go test ./...)
  if command -v hugo >/dev/null; then
    echo "==> hugo build (exampleSite, schema-pin gate)"
    (cd hugo/exampleSite && hugo --quiet --destination "$(mktemp -d)/public")
  else
    echo "==> hugo not installed; skipping exampleSite build" >&2
  fi
fi

# Phase one: root and hugo. Their tags must be public before backend can
# require the new root version with a real go.sum entry.
for fam in "" "hugo/"; do
  git tag -a "${fam}v$V" -m "lockstep release v$V (root + hugo + backend)"
done
git push origin main "v$V" "hugo/v$V"

# Phase two: lockstep by construction -- the published backend module
# requires the root module at this same version. GOPROXY=direct fetches
# the seconds-old tag straight from origin (the proxy lags new tags);
# GOSUMDB=off likewise, the go.sum hash comes from the download itself.
(cd backend && GOWORK=off GOPROXY=direct GOSUMDB=off go get "github.com/freeeve/libcat@v$V" && GOWORK=off go mod tidy)
if ! git diff --quiet backend/go.mod backend/go.sum; then
  git commit -m "release(backend): require root v$V in lockstep (tasks/146)" backend/go.mod backend/go.sum
fi
git tag -a "backend/v$V" -m "lockstep release v$V (root + hugo + backend)"
git push origin main "backend/v$V"

# Three releases shipped with local-only tags (tasks/139, 145, 152), so don't
# trust the push's exit status alone: confirm each tag ref is actually visible
# on origin, and fail loudly if any is missing.
for fam in "" "hugo/" "backend/"; do
  if ! git ls-remote --exit-code --tags origin "refs/tags/${fam}v$V" >/dev/null; then
    echo "release.sh: ${fam}v$V not visible on origin after push" >&2
    exit 1
  fi
done
echo "released v$V (root, hugo, backend) at $(git rev-parse --short HEAD); tags verified on origin"
