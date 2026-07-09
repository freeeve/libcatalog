# The local loop: no cloud, no CI

Everything below runs on a laptop with no AWS account, no containers, and no
credentials. For the container stack (MinIO + DynamoDB-local) see
[deploy.md](deploy.md).

## lcatd against a directory

`lcatd` needs a grain store and an abuse secret. With `LCATD_BLOB_DIR` it uses a
plain directory, and with no `LCATD_DYNAMO_TABLE` the document store is
in-memory (it resets on restart -- fine for a scratch instance, and the reason
the demo playground keeps its blob dir on disk).

```sh
cd backend
LCATD_LISTEN_ADDR=:8491 \
LCATD_BLOB_DIR=/tmp/lcat-blob \
LCATD_LOCAL_AUTH=1 \
LCATD_BOOTSTRAP_ADMIN=eve@example.org:changeme123 \
LCATD_ABUSE_SECRET=$(openssl rand -hex 16) \
go run ./cmd/lcatd
```

`LCATD_LOCAL_AUTH=1` mounts built-in user auth, and `LCATD_BOOTSTRAP_ADMIN`
seeds one admin on a fresh store. A fresh blob dir also seeds the default
copycat SRU targets. Vocabularies load before the listener opens, so the first
connection that succeeds means the server is warm -- poll `/v1/healthz` rather
than sleeping.

There is deliberately no `lcatd --dev` switch. Configuration is environment-only,
which is what Kubernetes `envFrom` and a compose `environment:` block consume
directly; a flag that bundled these defaults would be a second source of truth
that only the local path exercised, and the thing most worth exercising locally
is the configuration production actually uses.

## The three ways to serve the cataloging SPA

Pick by what you are doing, not by which is canonical -- all three are.

| Doing | Serve it with | Why |
|---|---|---|
| SPA development | `cd backend/ui && npm run dev` (Vite) | hot module reload |
| Integration demo | `npm run build`, then `go build ./cmd/lcatd` | `go:embed` picks up `dist/` |
| Production | the container image | same embed, built by the Dockerfile |

`backend/ui/dist` in git is a **placeholder**. A bare `go build` embeds that
placeholder, and the result starts cleanly and serves an empty page -- so always
`npm run build` first, and never commit the real build back over it. The
Dockerfile asserts the placeholder is gone before it compiles.

## The editorial cycle, live: lcatd -> hugo

The point of the local loop is to watch an edit made in the cataloging UI appear
in the discovery site a few seconds later, with no cloud and no CI in between.

`hugo server` already watches its data directory. `lcatd` already runs a command
when grains change (`trigger.Command`, behind `LCATD_REBUILD_CMD`). Point the
one at the other:

```sh
# terminal 1 -- the discovery site, watching its data dir
cd hugo/exampleSite && hugo server

# terminal 2 -- the backend, reprojecting into that data dir on every publish
cd backend
LCATD_BLOB_DIR=/tmp/lcat-blob \
LCATD_LOCAL_AUTH=1 \
LCATD_BOOTSTRAP_ADMIN=eve@example.org:changeme123 \
LCATD_ABUSE_SECRET=$(openssl rand -hex 16) \
LCATD_REBUILD_DIR=../.. \
LCATD_REBUILD_CMD='lcat serialize --dir /tmp/lcat-blob && lcat project --catalog /tmp/lcat-blob/catalog.nq --out hugo/exampleSite/data --provider marc' \
LCATD_REBUILD_DEBOUNCE=2s \
go run ./cmd/lcatd
```

Publish an edit in the admin UI; `lcatd` runs the command, `catalog.json`
changes under `hugo/exampleSite/data`, hugo's watcher live-reloads the page.

Three details that are easy to get wrong:

- `lcat serialize --dir` takes the **blob root** (the directory holding
  `data/works/`), not the grain directory itself, and it writes `catalog.nq`
  into that root.
- `lcat project --provider` must name a feed graph the catalog actually carries
  (`marc`, `overdrive`, ...), matching `LCATD_PROVIDER`. A provider matching no
  feed fails, names the feeds the catalog does carry, and leaves the published
  `catalog.json` alone rather than replacing it with an empty one -- pass
  `--allow-empty` if a zero-work catalog is genuinely what you want
  (tasks/246).
- `LCATD_REBUILD_DEBOUNCE` coalesces a burst of publishes into one rebuild -- a
  projection is not cheap and it is not reentrant. The changed grain paths
  arrive in `$LCAT_CHANGED_PATHS`, newline-joined, if you want a narrower
  rebuild.

`lcat` must be on `$PATH` (`go install ./cmd/lcat` from the repo root; note
`cmd/lcat` is at the root, not under `backend/`).

## Housekeeping

`lcat covers --store <blob-root>` reports cover blobs no grain references, and
`--reap` deletes them. It is read-only by default because it deletes public
images.
