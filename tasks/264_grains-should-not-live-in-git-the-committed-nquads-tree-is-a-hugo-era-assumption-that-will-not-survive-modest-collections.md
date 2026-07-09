# 264 -- grains should not live in git: the committed nquads tree is a hugo-era assumption that will not survive modest collections

Opened 2026-07-09, from the maintainer:

> perhaps we should consider not committing our nquads. it seems like that model
> harkens back to the hugo requirements. they should probably be managed
> separately, outside git. they'll grow too large even at modest collection
> sizes

## Who actually commits grains (checked, 2026-07-09)

**libcat does not.** The only tracked `.nq` in this repo is
`backend/vocab/testdata/authorities.nq`. `~/libcat-playground/site` is not a git
repo, and `hugo/exampleSite` has no `data/` directory -- `catalog.json` is a
build output.

**qllpoc does**: 5,660 grains, 6.0MB in the working tree (~1.1KB per grain
there; the playground's richer records average ~4KB across 1,716 grains). Its
`.git` is 81MB, though only **4 commits touch grains** -- it is an
ingest-once POC, so the edit-churn cost below is a projection there, not an
observation.

So the pattern is real but it lives in a deployment repo, and the language that
teaches it lives here.

## The size argument

Working tree, at 1-4KB per grain:

| Works | Grain tree |
|---|---|
| 5,660 (qllpoc) | 6MB |
| 50,000 | 50-200MB |
| 250,000 | 0.25-1GB |
| 1,000,000 | 1-4GB |

That is the **checkout**, and it is the smaller problem. Git stores a new object
for every version of every changed file, and a grain is rewritten in full on
every cataloger save -- `PUT /v1/works/{id}` and `POST /v1/works/{id}/ops` both
write the whole canonical grain. Cataloging is an editing workload, so the pack
grows with edits, not with records, and nothing prunes it. Delta compression
across canonical-sorted quads is kind; unbounded growth is still unbounded.

qllpoc has not felt this because nobody edits it. A real catalog would.

The maintainer is right about the provenance. `lcat serialize --dir <grains>`
still describes its input as **"committed grains"** in `cmd/lcat/main.go:108`,
which is the tell: the model came from the era when the catalog *was* a Hugo
site's `data/` directory and git was the only store. That string is now the main
thing teaching the pattern.

## What is actually coupled to git today

Very little, and that is the good news -- this is mostly a documentation and
default-deployment change, not a rewrite.

- `storage/blob.Store` is the seam. `blob.NewDir` (a directory) and `blob.NewS3`
  (any S3-compatible endpoint) are peers; `lcatd` picks by `LCATD_BLOB_DIR` vs
  `LCATD_S3_BUCKET`. Nothing in `bibframe`, `editor`, `workindex` or `httpapi`
  knows what git is.
- Optimistic concurrency is **ETag CAS through the blob store**
  (`blob.PutOptions.IfMatch`), not git. Two catalogers saving the same record
  are resolved by the store, which git could not do.
- `lcat rebuild` already drives incremental projection off a **feed cursor**, not
  a git diff.

So "grains in git" is a *deployment convention*, and one the code does not
depend on. What depends on it is the story we tell in the docs, the `lcat build`
pipeline's assumptions, and the phrase "committed grains".

## Why git is the wrong store here, beyond size

- **It cannot serve the reads.** `workindex` scans the corpus at boot and
  `GET /v1/works/{id}` reads one grain. Git gives you a working tree (which is
  just a directory -- fine) or object lookups (which are not addressed by our
  paths).
- **It cannot do the writes.** Concurrent CAS is the whole basis of the editor,
  the queue and the ingest lease (tasks/165). Git's concurrency primitive is a
  branch and a merge conflict.
- **The history it gives is not the history we want.** libcat already has an
  audit trail -- who changed what, when, with quad counts (tasks/239, 249, 257).
  A git log of canonical N-Quads rewrites is strictly worse: it records the
  bytes, not the actor, the action, or the intent, and it cannot answer "who
  last changed this record" without a blame over sorted quads.
- **It leaks.** Git history is append-only in practice. A cover blob, a patron's
  suggestion note, or a record withdrawn for a rights complaint stays in the
  pack forever. tasks/245 (`lcat covers --reap`) exists precisely because "we
  deleted it" must mean deleted.

## Scope

1. **Decide and state the intended store.** The blob store is the catalog; git
   holds code, editing profiles, mappings and deployment config. Write it down
   in `ARCHITECTURE.md` -- it is currently implied rather than said, which is how
   the Hugo-era default survived.
2. **Purge the "committed grains" language**, starting with
   `cmd/lcat/main.go:108` and any docs that describe the grain tree as
   checked-in.
3. **`lcat build` and the Hugo path**: the projected artifacts (`catalog.json`,
   `facets.json`, `redirects.json`) are *build outputs*. Decide whether they are
   committed (they are small, deterministic, and a static host wants them in the
   repo) or produced in CI. They are not grains and should not share the grain
   tree's fate.
4. **A backup/restore story that is not `git clone`.** This is the real thing
   git was providing for free, and removing it without a replacement is a
   regression. Object-store versioning + a periodic `lcat export` snapshot is the
   obvious shape; tasks/165 lists backup/restore for MinIO/Garage as open.
5. **Migration**: a documented path for any deployment whose grains are in a git
   repo today (the demo sites). `lcat` already reads a directory; the move is a
   copy plus a `.gitignore`, but the history question -- do we rewrite it out, or
   archive the repo -- is a decision, and it is a privacy decision as much as a
   size one.

## Non-goals

- Changing the on-disk grain format. N-Quads sharded under `data/works/<xx>/`
  stays; only where that tree lives changes.
- Removing `blob.NewDir`. A local directory is the right default for small
  deployments and for the whole local-dev loop (`docs/local-dev.md`); it just
  should not be a *git working tree*.

## Open questions

- **qllpoc is the one deployment with grains under version control** (5,660 of
  them). Migrating it is a decision for its owner, not for libcat: file a
  cross-repo ask once this repo's story is settled, rather than changing it from
  here. Its history is only 4 grain commits deep, so a migration is cheap today
  and gets more expensive the longer it waits.
- Small libraries with no object store: is a plain directory plus `lcat export`
  to a tarball enough, and do we still want *some* versioning for them?
- Is there a legitimate "catalog as a git repo" deployment we would be taking
  away -- a one-cataloger library that wants `git log` and no server? If so the
  answer is probably "supported, documented, and not the default", not
  "forbidden".

## Acceptance

- `ARCHITECTURE.md` states where the catalog lives and why git is not it.
- No user-facing string calls the grain tree "committed".
- A documented backup/restore recipe that does not rely on git.
- A migration note for any deployment that has grains under version control.
