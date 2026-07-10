# 325 -- cmd/lcat importing backend/vocab makes root depend on backend while backend depends on root -- release.sh does not keep root's backend require in lockstep, so lcat lags one backend version each release

Opened 2026-07-10.

## What happened

tasks/322 added `cmd/lcat/vocabgc.go`, which imports
`github.com/freeeve/libcat/backend/vocab` for `OrphanSidecars`/`RemoveSidecar`. That
is the **first** time any `cmd/lcat` file imports the backend module -- every other
file uses root-module packages (`bibframe`, `storage/blob`, `project`).

`go.work` masked the consequence. In the workspace the backend module is overlaid
locally, so `go build`/`go test` never needed a `require`, and v0.139.0 shipped with a
root `go.mod` that does not require `backend`. Outside the workspace it does not build:

```
$ GOWORK=off go build ./cmd/lcat        # at v0.139.0
cmd/lcat/vocabgc.go:11:2: no required module provides package
        github.com/freeeve/libcat/backend/vocab; to add it: go get ...
```

So `go install github.com/freeeve/libcat/cmd/lcat@v0.139.0` fails. This is the same
class my own note warned about -- go.work hiding a module-graph problem -- and I
walked straight into it.

The immediate breakage is fixed (a follow-up commit adds the `require`, released as
the next patch). This task is the **design decision** that fix does not resolve.

## The real problem: the edge runs the wrong way

The release model (tasks/146, tasks/185) is built on a DAG: **backend depends on
root**. `release.sh` phase two does, in the backend module,
`go get github.com/freeeve/libcat@v$V && go mod tidy`, so every release the published
backend requires the root at the same version. Root has a `replace`-free go.mod so
`go install pkg@version` works for adopters; backend rides that one-way edge.

`cmd/lcat -> backend/vocab` adds the **reverse** edge, root -> backend. It is not a
hard build cycle (Go's main-module-wins rule means a build of root uses the local
root for backend's own `require github.com/freeeve/libcat`, so nothing loops), but:

- **release.sh never updates root's require of backend.** It only bumps backend's
  require of root. So when v0.140.0 is cut, root@v0.140.0 will still
  `require backend@v0.139.0` (whatever `go mod tidy` last resolved), and `lcat` at
  v0.140.0 runs v0.139.0's `backend/vocab`. It self-heals to (current - 1) each
  release, never to current.
- **The two-phase tag order cannot express root -> backend@sameversion.** Root is
  tagged in phase one, before backend@v$V exists; it cannot require a tag that is not
  yet cut. Making it truly lockstep would need a third phase that re-points and
  re-tags root, which the current design deliberately avoids.

Today the lag is harmless -- `OrphanSidecars`/`RemoveSidecar` are stable and
v0.139.0's copy is fine. It becomes a bug the first time a `backend/vocab` change has
to reach the CLI in the same release that ships it.

## Options

1. **Relocate the sidecar layout + GC into a root leaf package.** Move
   `SidecarManifest`, `sidecarPath`, `sidecarSuffixes`, `RemoveSidecar`,
   `OrphanSidecars` into e.g. `storage/vocabsidecar` (root module). `backend/vocab`
   imports it (backend already depends on root, so that edge is legal), and so does
   `cmd/lcat`. Both edges point **into** root; the DAG is preserved and there is no
   lag. Cost: a real move touching `BuildSidecar` (writes via `sidecarPath`/
   `sidecarSuffixes`) and the loader (reads manifests). The tasks/252 drift guard
   moves with `sidecarSuffixes`, so it keeps protecting the same invariant.
   *This is my recommendation* -- it fixes the direction rather than papering the lag.

2. **Move `vocab-gc` off the root CLI.** Make it an lcatd admin route (the task/322
   option 2) or a backend-hosted command, so root never imports backend. Loses the
   "offline, no server" property that made option 1 attractive in 322, and leaves
   `lcat` without the command.

3. **Keep the edge; teach release.sh to lockstep root's backend require.** A third
   release phase re-points root at backend@v$V and moves the root tag. Fights the
   tasks/185 ordering and adds a re-tag; the least appealing.

## Not urgent, but do not leave it latent

The install breakage is patched. The lag is dormant until a `backend/vocab` change
needs to ship to the CLI atomically. Option 1 is the durable fix and is the kind of
move worth doing before a second `cmd/lcat` file reaches into `backend` and makes the
entanglement harder to unpick.
