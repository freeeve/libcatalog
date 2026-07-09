# 246 -- lcat project with a provider matching no feed writes an empty catalog.json and exits 0

Opened 2026-07-09. Found while documenting the rebuild loop for tasks/054.

## Context

`lcat project --provider <name>` selects which feed graph to project. When the
name matches no feed in the catalog, it does not fail. It projects zero works,
writes an empty `catalog.json`, `facets.json`, and `redirects.json` over
whatever was there, and exits `0`:

    $ lcat project --catalog catalog.nq --out data          # default: --provider overdrive
    projected 0 works to data (schema v11); facets: 0 languages, 0 subjects, 0 contributors; 0 redirects
    $ echo $?
    0

The catalog in that run held one work, under `<feed:marc>`. With `--provider
marc` the same command projects it correctly.

The empty-string case *is* caught (`lcat project: no feeds to project`), so the
guard exists -- it just does not cover "a provider that names nothing".

## Why it matters

This is the command `LCATD_REBUILD_CMD` runs on every publish
(`docs/local-dev.md`). A deployment whose `--provider` does not match its feed
-- a typo, a feed renamed, a catalog ingested under `marc` while the rebuild
command still says `overdrive` -- republishes an empty discovery site on the
next edit, reporting success. The failure is silent, it is triggered by an
unrelated action (a cataloger publishing an edit), and its blast radius is the
whole public site.

Nothing downstream would catch it: hugo builds an empty catalog happily, and the
rebuild loop only checks the command's exit status.

## Scope

- `lcat project` fails, non-zero, when `--provider` names only feeds the catalog
  does not contain, naming the feeds it *does* contain. Distinguish that from a
  catalog which legitimately projects zero works (an empty grain store).
- Prefer failing over writing: do not truncate `catalog.json` on the error path.
- A `--allow-empty` escape hatch if a deployment genuinely projects an empty
  catalog (first boot before any ingest).

## Acceptance

- `lcat project --catalog <a catalog with only feed:marc> --provider overdrive`
  exits non-zero, says which feeds exist, and leaves the previous
  `catalog.json` on disk.
- `--provider marc` on the same catalog still projects the work.
- An empty grain store with a matching provider still succeeds (or requires
  `--allow-empty`, whichever the implementation picks -- decide and test it).
- Regression: `lcat project` with no `--provider` keeps its current error.
