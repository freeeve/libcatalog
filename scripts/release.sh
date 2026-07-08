#!/usr/bin/env bash
# release.sh vX.Y.Z -- tag and push a lockstep release (tasks/146).
#
# The repo ships three Go modules (root, hugo/, backend/) whose tag families
# drifted after v0.7.2, leaving consumers to maintain pairing tables by hand.
# One release = one number everywhere: this script verifies the tree, runs
# the Go suites and the exampleSite build (the schema-pin compatibility
# gate), syncs backend/go.mod's root requirement to the release version (so
# a proxy consumer of the backend module resolves the matching root by
# construction; the local `replace ../` keeps in-repo dev on the working
# tree), then tags v<V>, hugo/v<V>, backend/v<V> at HEAD and pushes them
# with main in a single command.
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

# Lockstep by construction: the published backend module requires the root
# module at this same version.
(cd backend && go mod edit -require="github.com/freeeve/libcat@v$V")
if ! git diff --quiet backend/go.mod; then
  git commit -m "release(backend): require root v$V in lockstep (tasks/146)" backend/go.mod
fi

for fam in "" "hugo/" "backend/"; do
  git tag -a "${fam}v$V" -m "lockstep release v$V (root + hugo + backend)"
done

git push origin main "v$V" "hugo/v$V" "backend/v$V"

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
