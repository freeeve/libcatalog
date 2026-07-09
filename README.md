<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="hugo/static/logo-dark.svg">
    <img src="hugo/static/logo.svg" alt="libcat logo: a cat sitting on a stack of books" width="140">
  </picture>
</p>

# libcat

A generic framework for building **library discovery catalogs** as fast, static,
faceted websites -- with BIBFRAME as the source of truth and an optional
collaborative cataloging backend.

**Live demo:** [libcat.evefreeman.com](https://libcat.evefreeman.com) --
"Eve's Library", a public adopter site built on the framework + Hugo module.

libcat is the *framework*. A deployment (for example a queer-literature
library on OverDrive) *themes and implements* it: it brings its own collection,
controlled vocabularies, provider feeds, and branding. Nothing library-specific
lives in here.

## What it is

- **Source of truth: BIBFRAME** (RDF) in git -- Work/Instance-native
  bibliographic data, not flat records. Everything else (HTML, search index,
  MARC/MODS exports) is a derived build artifact.
- **Static catalog as a Hugo module** -- drop the catalog into a library's
  existing Hugo site; the rest of their web presence stays their own.
- **Powered by two sibling libraries:**
  - [`libcodex`](https://github.com/freeeve/libcodex) -- MARC / MODS / Dublin
    Core / schema.org / BIBFRAME read, write, convert (the interchange, import,
    and export engine).
  - [`roaringrange`](https://github.com/freeeve/roaringrange) -- the **advanced**
    search index and reader (lexical BM25; semantic embeddings opt-in) for
    large-corpus / custom-ranking deployments. The recommended static search is
    **Pagefind** over the built HTML (tasks/017); out of the box the module
    falls back to a small client-side filter until a site opts in.
- **No triplestore, no database for the static tier** -- files in git, files on
  S3/CloudFront. No paid AI in the default build.

## Two tiers

- **Tier 1 -- static, self-serve (no backend).** Point the projector at a MARC
  or BIBFRAME dump and get a faceted, searchable, multilingual catalog site.
  The onboarding ramp is **MARC import** (via libcodex): bring the MARC your
  ILS already exports.
- **Tier 2 -- dynamic, optional.** A collaborative in-browser cataloging/review
  app (auth, roles, edit history) that writes BIBFRAME back to the grain store.
  Cloud infra; self-hosted or SaaS. Lives in [`backend/`](backend/), a **nested
  Go module** (`github.com/freeeve/libcat/backend`) so its cloud SDKs never
  reach the core's dependency tree -- CI builds and tests the root module and
  `backend/` separately (as with `hugo/`). Serve it with `backend/cmd/lcatd`
  (container/self-host) or `backend/cmd/lcatd-lambda` (AWS Lambda behind an
  API Gateway v2 HTTP API); both wrap the same `net/http` handler.

Because the BIBFRAME graph is the contract between them, **Tier 1 runs with zero
Tier 2.**

See [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) and
[docs/ROADMAP.md](docs/ROADMAP.md). The Tier 2 HTTP surface is enumerated in
[docs/api.md](docs/api.md), generated from the router. For building a catalog
from a [Hardcover](https://hardcover.app) reading shelf, see
[docs/hardcover-provider.md](docs/hardcover-provider.md).

**Adopters don't write Go.** One `lcat.toml` describes the whole build --
sources (MARC files, mapped N-Quads/CSV exports, OverDrive), projection with a
public-provenance allowlist, downloadable exports, search index, Hugo -- and
`lcat build` runs it end to end. See
[docs/build-pipeline.md](docs/build-pipeline.md). The Go provider seam stays
for bespoke sources.

## Deployment styles

The two tiers unbundle into a ladder of shapes; pick by how much of the
dynamic surface you need. Memory numbers are indicative, from measured
deployments.

- **Static only (zero server).** The projected site is plain files: host it
  on any static host, object storage bucket, or CDN. Client-side search
  (roaringrange or Pagefind) and live availability both run in the browser,
  so a full faceted catalog needs **no server process at all**. Projection is
  a batch step, not a resident cost -- run it in CI or on a laptop (peak
  ~57KB/work), and the feed-driven incremental rebuild keeps publishes from
  reprojecting the corpus.
- **Read-only backend (demo/sandbox).** `lcatd` in read-only mode serves the
  cataloging UI without write workers; container or Lambda
  (`backend/deploy/terraform/modules/readonly-demo`).
- **Writable backend.** A single long-lived `lcatd` container with the
  in-memory store snapshotting to blob storage; the store and blob clients
  also speak DynamoDB/S3-compatible APIs (`LCATD_AWS_ENDPOINT`), so AWS,
  MinIO/Garage, or ScyllaDB are interchangeable deployment concerns.
- **What sets the backend's memory: vocabularies, then catalog size.** The
  resident work index holds summaries and identity keys (not the graph), so
  small and mid catalogs are cheap; the controlled-vocabulary term index is
  the dominant block. Full LCSH+LCSHAC+LCGFT resident is ~1.2GB before the
  first work -- a comfortable-cataloging shape wants a few GB. To run in
  ~**1GB or less**, install subset vocabulary snapshots instead: `lcat
  vocab-subset` scopes a vocabulary to the concepts your catalog actually
  uses (plus small vocabularies like Homosaurus whole), collapsing the index
  to tens of MB.

## Status

Working, with versioned releases. Both tiers are implemented:

- **Tier 1** ships end to end: MARC / Hardcover / OverDrive ingest to
  canonical BIBFRAME grains, projection to `catalog.json`/`facets.json`, and
  the Hugo module's faceted, multilingual, Pagefind-searchable site with
  optional live availability (OverDrive/Thunder, DAIA). "Eve's Library" is
  the live adopter built on it.
- **Tier 2** is the `lcatd` backend serving an embedded cataloging SPA:
  a dual-view (BIBFRAME doc / MARC) record editor with editing profiles,
  dry-run previews, and duplicate warnings; copy cataloging over SRU/Z39.50
  with staged batches, overlay policies, and revert; batch edits, macros,
  and saved queries; authority control with local headings and installable
  vocabulary snapshots (LCSH-scale); moderated patron suggestions and tag
  promotion; exports (MARC, N-Quads, JSON-LD, CSV); and a read-only sandbox
  mode for public demos. Auth is built-in local users and/or OIDC. Deploy as
  a container (`backend/cmd/lcatd`) or AWS Lambda (`backend/cmd/lcatd-lambda`);
  `backend/deploy/terraform/modules/readonly-demo` stands up the demo shape.

The reference implementation was migrated out of qllpoc (that repo's
`tasks/038`--`044`); libcat is now developed in place, tracked in
[`tasks/`](tasks/).

## Versioning

The root, `hugo/`, and `backend/` modules release in **lockstep**: every
release tags all three with the same version at the same commit
(`scripts/release.sh vX.Y.Z` tags and pushes the triple). The version number
IS the compatibility contract -- pin one number across every libcat
dependency and the projection-schema pairing (the `catalog.json` version the
projector emits and the Hugo module targets) is guaranteed by construction;
the released backend module requires the root module at its own version for
the same reason. Lockstep (re)starts at `v0.19.0`: earlier numbers come from
three independent tag families, several of them never published -- treat
anything below v0.19.0 as historical and do not pin across modules with
them.

## License

MIT -- Copyright Eve Freeman.
