# Hardcover ingest provider

`ingest/hardcover` is a first-party ingest provider (ARCHITECTURE §9a) that builds a
catalog from a user's [Hardcover](https://hardcover.app) **"Read"** shelf. It pulls the
shelf over Hardcover's GraphQL API and runs it through the same identity/clustering +
`lcat project` path as every other source -- so clustering, format collapsing, controlled
subjects, and facet counting are the framework's, not a bespoke script (tasks/026).

## Two-step refresh

```sh
# 1. Ingest the Read shelf into canonical BIBFRAME grains + catalog.nq.
export HARDCOVER_API_TOKEN=…            # from hardcover.app; never written to disk
lcat hardcover --out build/

# 2. Project the graph into the site's data (catalog.json + facets.json + redirects.json).
lcat project --catalog build/catalog.nq --provider hardcover --out my-site/assets/
```

`lcat hardcover` is a convenience alias for `lcat ingest --provider hardcover` that also
supplies the token and page size. Flags: `--out` (grain/catalog.nq directory, required for
ingest), `--token` (or `$HARDCOVER_API_TOKEN` / `$HARDCOVER_TOKEN`), `--limit` (GraphQL page
size, default 100), `--feed` (provenance graph, default `feed:hardcover`), `--introspect
<type>` (dump a GraphQL type's fields, since the schema drifts), and `--source` (see below).

Re-ingest is stable: grains already under `--out` seed the resolver, so unchanged books keep
their minted ids (and their public URLs) and only genuinely new records mint.

### Offline replay (and CI)

`--source <file>` replays a captured `user_books` JSON array instead of calling the API --
useful for a reproducible rebuild or a test:

```sh
lcat hardcover --source shelf.json --out build/        # no token, no network
```

The golden test uses this seam (`ingest/hardcover/testdata/read-shelf.json`), so the
crosswalk is exercised end-to-end through `ingest.Run` → `project` with no network.

## What it maps

- **Editions → formats.** A book's editions collapse to one Instance per discovery format
  (physical → `print`, audiobook, ebook), keyed by `reading_format_id` with an
  edition/physical-format text fallback. A book's formats **cluster into one Work** (shared
  author/title/language key) with an Instance each, so a title read as both ebook and
  audiobook appears under both formats (tasks/011). ISBNs are cross-provider merge keys; each
  Instance also carries a `hardcover` source-tagged provenance id.
- **Contributors.** `contributions[]` → agents, names normalized to `Last, First`, role from
  the contribution (default `author`), deduped; the first credited agent is primary.
- **Genres → subjects + tags.** `cached_tags.Genre` (most-voted first, deduped, capped at 8).
  A genre that maps in the shipped table (`ingest/hardcover/subject-map.json`) becomes a
  **controlled subject** -- an authority URI (LCSH / Homosaurus) with localized labels and
  `skos:broader` parents (tasks/012/015); an unmapped genre stays an uncontrolled **tag**, so
  both dimensions coexist. The table is data-driven -- edit the JSON to extend it.
- **Display extras.** `cover` (book image, else the first edition's), `rating`, `dateRead`
  (last read date, else first), and `description` ride through the feed provenance graph to
  `catalog.json`'s `extra` object (tasks/026), which the Hugo module forwards to page params
  (tasks/022) and the default theme renders as cover art (tasks/025).

Languages default to `eng` (Hardcover rarely exposes language). Output is schema v5, the
version the Hugo module consumes.

## For the "Eve's Library" demo

The `libcat-demo` adopter originally built its catalog with three Node scripts
(`scripts/fetch-hardcover.mjs`, `map-subjects.mjs`, `gen-facets.mjs`, driven by
`npm run data:refresh`). This provider replaces all three: the demo's `data:refresh` becomes
the two `lcat` steps above, so it can drop `scripts/*.mjs`, the `data:*` npm scripts, and the
`npx` build step from its refresh and deploy.
