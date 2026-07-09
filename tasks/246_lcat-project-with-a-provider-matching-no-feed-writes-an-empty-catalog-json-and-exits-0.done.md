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

## Outcome

Shipped in **v0.99.0** (`9e595bf`).

Projecting zero works is now an error, raised **before anything is written**, so
the published `catalog.json` survives a bad run. The message names the feeds the
catalog does carry, because otherwise the fix ("which provider, then?") is a
guessing game:

    $ lcat project --catalog catalog.nq --out data      # default --provider overdrive
    lcat project: projected 0 works: --provider overdrive matches no feed in the
    catalog, which carries marc. Refusing to overwrite catalog.json with an empty
    catalog; pass --allow-empty to do it anyway
    $ echo $?
    1

Decisions taken where the task left them open:

- **A feedless catalog is its own error**, not the same one: "the catalog
  carries no feed graphs at all". There is no provider that would have worked,
  so naming the present feeds would print an empty list.
- **Zero works always fails, `--allow-empty` always excuses it** -- rather than
  only guarding the provider-mismatch case. A `serialize` that produced an empty
  `catalog.nq` is just as destructive in a rebuild loop as a typo'd provider,
  and a fresh deployment projecting before its first ingest is the one case that
  genuinely wants an empty catalog. It should have to say so.
- **A partial match warns and proceeds.** The merge is first-feed-wins, so a
  missing feed among several contributes nothing; failing the whole run would
  punish a valid multi-feed config for one typo. The warning names it, since
  otherwise it is entirely silent.
- `lcat build` passes `allowEmpty=false`: it ingests before it projects, so zero
  works means the pipeline produced nothing.

`project.Feeds(catalogNQ)` is new: it reports the feed graphs a catalog carries,
which is the fact the error message needed.

### Verification

`TestProjectRefusesToEmptyTheSiteOnAProviderTypo` was written as the reporter's
repro and **proven to fail against the pre-fix behavior** by stubbing the guard
out: the mutated build printed `projected 0 works` and exited 0, exactly as
reported. The same mutation fails `TestProjectOnAFeedlessCatalogSaysSo`.

Also run against the real store the bug was found on: the marc-only catalog
projected as `overdrive` now exits 1, and the previously published
`catalog.json` (1 work) is still on disk afterward. `--provider marc` still
projects it; `--allow-empty` still writes the empty catalog; `--provider
marc,overdrve` warns and projects the one work.

Six tests in `cmd/lcat/project_empty_test.go`, three in `project/feeds_test.go`,
including the regression that a bare `--provider ''` keeps its original
`no feeds to project` error.
