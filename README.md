# libcatalog

A generic framework for building **library discovery catalogs** as fast, static,
faceted websites -- with BIBFRAME as the source of truth and an optional
collaborative cataloging backend.

**Live demo:** [libcatalog.evefreeman.com](https://libcatalog.evefreeman.com) --
"Eve's Library", a public adopter site built on the framework + Hugo module.

libcatalog is the *framework*. A deployment (for example a queer-literature
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
    large-corpus / custom-ranking deployments. The default static search is
    **Pagefind** over the built HTML (tasks/017).
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
  Go module** (`github.com/freeeve/libcatalog/backend`) so its cloud SDKs never
  reach the core's dependency tree -- CI builds and tests the root module and
  `backend/` separately (as with `hugo/`). Serve it with `backend/cmd/lcatd`
  (container/self-host) or `backend/cmd/lcatd-lambda` (AWS Lambda behind an
  API Gateway v2 HTTP API); both wrap the same `net/http` handler.

Because the BIBFRAME graph is the contract between them, **Tier 1 runs with zero
Tier 2.**

See [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) and
[docs/ROADMAP.md](docs/ROADMAP.md). For building a catalog from a
[Hardcover](https://hardcover.app) reading shelf, see
[docs/hardcover-provider.md](docs/hardcover-provider.md).

## Status

Planning / bootstrapping. The reference implementation is being migrated out of
qllpoc -- see that repo's `tasks/038`--`044`.

## License

MIT -- Copyright Eve Freeman.
