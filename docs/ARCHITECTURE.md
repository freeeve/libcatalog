# libcatalog architecture

## 1. Framework vs. implementation

libcatalog is a **generic framework**: the reusable machinery for turning a
bibliographic graph into a fast, faceted, static discovery catalog, plus an
optional collaborative cataloging backend.

A **deployment** implements the framework -- it supplies the collection, the
controlled vocabularies, the provider feeds, the branding/theme, and any local
extensions. The reference implementation is **qllpoc** (a queer-literature
library on OverDrive/Libby, cataloged with the Homosaurus vocabulary).

The split is deliberate and load-bearing:

- Nothing library-specific lives in libcatalog. OverDrive is *one* provider;
  Homosaurus is *one* vocabulary; queer-lit branding is *one* theme.
- A deployment is a thin layer -- config + theme + local extensions -- depending
  on `github.com/freeeve/libcatalog`.
- Anything that turns out generic migrates *down* out of a deployment into the
  framework.

## 2. Design principles

- **BIBFRAME is the source of truth.** Records are an RDF graph (Work/Instance
  native), committed to git. HTML, search index, MARC/MODS exports, and JSON
  projections are all derived build artifacts.
- **No lossy intermediary.** No per-record markdown/frontmatter. HTML is
  generated from the graph (via a projection), not from a flattened copy of it.
- **Few dependencies; own the core.** Go + libcodex + roaringrange. No
  triplestore, no database for the static tier.
- **Zero paid-API baseline.** The default build needs no cloud AI. Semantic
  embeddings are opt-in (see 8).
- **The graph is the contract.** The static tier and the dynamic backend
  communicate only through the committed BIBFRAME graph, so either can exist
  without the other.
- **Accessible by default.** The Hugo module ships WCAG-conformant markup --
  semantic HTML, ARIA on the facet/search UI, full keyboard navigation, adequate
  contrast -- as a build-time constraint, not a per-deployment afterthought.

## 3. Source of truth: BIBFRAME in git

- Canonical form: **per-Work files in N-Quads** under
  `data/works/<xx>/<workid>.nq`, sharded by an id prefix so no directory holds
  100k+ entries. Each file carries the Work and its Instances; provenance rides
  on the 4th column (named graph), so `feed:<provider>` and `editorial:`
  statements coexist in one per-Work-diffable file (see 5). Shared authorities
  (subjects, agents) are not Work-scoped -- they live under
  `data/authorities/<xx>/<id>.nq`, referenced by IRI.
- **One format, one writer.** N-Quads is chosen so a single libcodex writer
  produces both the per-Work grains and the bulk **`catalog.nq`** -- same format,
  no conversion step. The bulk dump is not a byte-concatenation of the grain
  files: each grain canonicalizes its blank nodes to `_:c14nN` independently, so
  concatenating would merge distinct blanks that share a label. `catalog.nq` is
  instead re-serialized with corpus-wide unique blank scope (one shared encoder
  across all records). (Turtle cannot carry named graphs; TriG would be an extra
  serializer to build in libcodex; N-Quads' 4th column gives provenance for
  free.)
- **Clean diffs require deterministic labeling.** Blank nodes are given stable
  identity via **RDFC-1.0** (the W3C RDF Dataset Canonicalization algorithm,
  formerly URDNA2015), then skolemized to stable IRIs derived from those
  canonical labels, and statements are sorted. Re-serializing an unchanged Work
  is then a no-op diff, and a one-triple edit churns one line. This determinism
  is the linchpin of the whole git/PR-review story -- a Phase 0 acceptance gate,
  not a serialization detail.
- The graph holds **stable bibliographic + authority** data only. Volatile
  circulation state is excluded (see 5).
- Scale: a mid-size collection is a few hundred thousand triples -- trivially
  in-memory. No SPARQL server.
- Large collections: beyond ~10M records the whole-graph-in-memory assumption no
  longer holds -- switch to out-of-core grain builds and a **split-set** search
  index (roaringrange's bounded-RAM `build_term_splitset` / out-of-core impact
  builder + the `splits` reader), and do an explicit **scale review** at that
  point. The search/index stack has been exercised at ~10M media records, 15k
  libraries, and 400M availability records (deeplibby); note availability at that
  scale stays live and out of the graph (see 5).

## 4. Identity: two-tier Work / Instance

BIBFRAME-native, matching FRBR/LRM:

- **Instance (manifestation)** -- one per edition/format. Carries ISBNs,
  provider ids (e.g. OverDrive), OCLC#, and links to live availability. The
  borrowable/holdable unit.
- **Work** -- groups all instances (editions, translations, formats) of one
  intellectual creation. The discovery unit: one Work page with
  format/language facets instead of N near-duplicate cards.
- Opaque, provider-independent ids at both levels, minted once and never derived
  from a provider id.
- **Work clustering** is an enrichment step:
  1. External work ids where available -- OpenLibrary (`OL...W`, free,
     ISBN-keyed) primary; OCLC Work ids where licensed; LC name-title authority
     for the authorized heading.
  2. Computed fallback -- normalized author + normalized title (+ original
     language), i.e. the MARC 1XX+240 access-point key. The deterministic
     default when no external id matches.
- **Clustering is correctable, not final.** OpenLibrary coverage is uneven and
  the computed key both over-merges (reissues, common titles) and under-merges
  (translations, name variants), so mis-clusters are fixed in the `editorial:`
  graph via an explicit merge/split overlay that the computed key cannot
  override. Clustering is an ongoing maintenance surface, not a one-shot. Because
  a Work's public URL derives from its opaque id, a merge or split must leave a
  redirect/tombstone so shared links and SEO survive (see `tasks/001`).

## 5. Data & provenance model

- **Named graphs for provenance.** Each statement's 4th column names its origin:
  `feed:<provider>` (regenerated on ingest, never hand-edited) vs `editorial:`
  (human/authority-owned, preserved across ingest). Clobber-safe re-ingest is a
  within-file rewrite of each affected Work grain -- drop the lines whose graph
  is `feed:<provider>`, append the freshly generated feed lines, keep every
  `editorial:` line, re-canonicalize. Weighting the graph column early in the
  sort keeps feed and editorial lines in separate regions, shrinking the
  merge-conflict surface between a re-ingest and a concurrent editorial edit.
- **Availability stays out of the graph.** Available copies / holds / estimated
  wait are live and volatile; they are fetched client-side at view time via a
  provider's availability adapter (see 9), never committed. The graph is
  diffable precisely because it excludes them.
- **Extend, don't fight the model.** Anything BIBFRAME 2.0 doesn't cover uses a
  framework namespace (`lcat:`); a deployment adds its own (e.g. `qll:`). RDF is
  open-world -- extensions never require forking the ontology.
- **Controlled vocabularies are linked data.** Subjects are URIs
  (`bf:subject <vocab-uri>`); a global relabel propagates for free because
  records reference authorities by id, not string. Homosaurus is one such
  vocabulary (already SKOS/JSON-LD); LCSH/LCNAF stream in via libcodex's RDF
  decoder.

## 6. Two product tiers

### Tier 1 -- static, self-serve (no backend)

Point the projector CLI at a **MARC or BIBFRAME dump** and get a faceted,
searchable, multilingual catalog site. Onboarding ramp: **MARC import via
libcodex** -- bring the MARC your existing ILS (Koha/Sierra/...) already
exports. Pure static output; no cloud infra beyond static hosting.

### Tier 2 -- dynamic, optional (collaborative cataloging)

An authenticated in-browser cataloging/review app (roles, edit history/audit)
that writes BIBFRAME back to git. Cloud infra: an API + serverless functions +
a small datastore + a git content store + OIDC auth. Distribution is a product
decision -- **self-hosted** (Terraform in the library's own cloud) or **SaaS**
(multi-tenant). Optional, because the graph is the contract.

## 7. Static tier: projector CLI + Hugo module

Hugo stays -- as the library's whole website. The catalog is a *component*
inside it, so a deployment keeps its non-catalog pages (hours, events, about) in
ordinary Hugo, themed as it likes. Hugo has no runtime plugin/exec model, so the
catalog ships as two distributable artifacts:

1. **Projector CLI** (`lcat`, Go, over libcodex + roaringrange):
   `BIBFRAME (git) -> catalog data (JSON) + search index`. Also the
   import/export front door (MARC/MODS/BIBFRAME in and out). Its core writes
   through a storage `Sink` abstraction (local dir / object storage / git), so
   the same binary runs as a local build, a container/Fargate task, or a cloud
   function -- cloud SDKs stay out of the baseline. Grains land in git (the
   source of truth); derived artifacts (`catalog.nq`, projected JSON, index) in
   object storage.
2. **Hugo module** (`hugo mod get github.com/freeeve/libcatalog/hugo`): catalog
   layouts, partials (facets, vocabulary picker, live-availability + search JS
   assets), and a **content adapter** (`_content.gotmpl`, Hugo >= 0.126) that
   mints a Page per Work from the projected data -- no content files, no
   per-record markdown.

Pipeline:
`BIBFRAME graph -> [projector] -> catalog JSON + search idx -> Hugo (module
content adapter + theme) -> static HTML -> S3/CloudFront`.

Downstream is tool-stable: the search reader runs over the emitted index; live
availability is client-side JS; the dynamic backend (if present) only touches
the graph.

## 8. Search: roaringrange, embeddings opt-in

- **Default: lexical.** roaringrange's BM25 / `terms` path (the Rust crate's
  `terms` feature; WASM reader in the browser). **No embeddings, no paid AI, no
  Bedrock** in the default build -- a library can stand up a good catalog with
  zero cloud-AI dependency.
- **Multilingual scope (stated precisely).** Tokenization is Unicode-correct --
  maximal runs of Unicode alphanumerics, full Unicode lowercasing -- and so
  script-agnostic, but it does **no word segmentation**. Space/delimiter scripts
  (Latin, Cyrillic, Greek, Arabic, ...) tokenize into words; scriptio-continua
  scripts (CJK, Thai, Khmer, Lao) collapse an unbroken run into a single term, so
  word-level BM25 does not work for them -- route those to the trigram (`RRS`)
  n-gram index, which also serves substring/fuzzy. Stemming is Snowball: 18
  languages are defined in the format and the Rust build/reader (en es ar da nl
  fi fr de el hu it no pt ro ru sv ta tr), but the Go projector currently wires
  only English, so the other 17 need Go-side wiring (or building the index with
  the Rust builder). An `RRTI` index carries **one stemmer language** (a single
  header byte), so a multilingual corpus is built as **per-language stemmed
  indexes** routed by each record's declared language (`dcterms:language` / MARC
  041) via a small **language->index map**: the 18 Snowball languages get stemmed
  indexes, **unsupported (non-Snowball) languages get their own index** (unstemmed
  word-level, or the trigram (`RRS`) index for unsegmented scripts like CJK), and
  records with no language signal fall back to the unstemmed index. The map lets a
  query hit only the relevant index(es). There is no per-document language
  *detection* -- routing keys on the declared language. Stop-word removal is
  optional and **English-only** today. See `tasks/005`.
- **Opt-in: semantic.** The vector/embedding arm (model2vec / provider
  embeddings) is a build flag, off by default. Enabling it requires the
  deployment to supply an embedding provider and accept its cost. The framework
  never turns it on implicitly.
- Rationale: keep the adoptable baseline free and self-contained; make the
  higher-recall arm a deliberate upgrade.

## 9. Ingest / providers

A **provider** plugs in two halves that share one id contract:

- **Ingest half (build-time).** Maps a provider feed into `bf:Instance` triples
  under `feed:<provider>`, carrying the ids the availability half keys on.
  OverDrive/Libby (Thunder API) is the reference provider; MARC import (libcodex)
  is a first-class provider for existing-ILS onboarding; physical-holdings ILSes
  export MARC -> same path.
- **Availability half (runtime, client-side).** Given an Instance's
  `feed:<provider>` ids, fetches live availability at view time and maps it to a
  **normalized availability model** -- status, copies owned/available, holds,
  estimated wait (digital); location/call-number/due-date (physical); a
  borrow/hold action URL. The UI renders one model regardless of source, which
  generalizes the OverDrive-specific check in 5 to *any* library source.
- **Transport declared per provider.** An adapter states its transport --
  `direct` client fetch when the source is CORS-enabled and unauthenticated
  (e.g. Thunder's public availability check) vs `proxied` through a thin
  edge/serverless function -- and its auth mode (`none` / public key / scoped
  token). Scoped-token sources must be proxied so no secret ships in client JS.
  Direct keeps Tier 1 backend-free; the proxy is the escape hatch when a source
  won't allow browser calls.
- **Batched, cached, best-effort.** A results page needs N lookups, so the
  interface is batch-first (one call per provider per page) with a short
  client-side TTL. Availability is never in the graph, so a failed or
  rate-limited fetch degrades gracefully to "unknown -- check provider," never a
  broken page.
- **Merge.** New feed items attach to an existing Instance by ISBN, and to a
  Work by the clustering rules in 4.

## 9a. Extension model: providers as a compile-in registry

Providers (9) and enrichers plug in at **compile time**, not via dynamic loading.
libcatalog is a Go framework a deployment depends on (1, 10), so the deployment
already builds its own binary -- adding a provider is Go code compiled into it,
registered against an interface, with no plugin ABI.

- **Interface + registry.** A `Provider` declares its name, its role (ingest ->
  `feed:<name>`, or enrich -> editorial/enrichment) and a `Run` that emits an
  `rdf.Dataset`. First-party providers (OverDrive, MARC, OPDS) ship in-tree and
  the default `cmd/lcat` registers them.
- **Deployment extension.** A deployment with a novel feed writes a Go package
  and a small `main` that composes its own projector --
  `lcat.Run(overdrive.New(), marc.New(), myprovider.New())` -- with explicit
  registration in `main`, not `init()` side-effects. Same "thin layer depending
  on the framework" story as 10.
- **Runtime half needs no recompile.** Availability adapters are client-side JS
  the Hugo module ships and config-gates, so a deployment can add its own adapter
  without touching Go. Only the build-time half is compile-in.
- **Config is unified.** `lcat`'s config is the single source of truth: it lists
  enabled build-time providers and enabled runtime adapters, and emits the
  runtime adapter-enable list into the Hugo build -- configure once, both halves
  stay in sync.
- **Why compile-in.** The provider universe is finite and known (ebook platforms,
  ILS-via-MARC, OPDS, authority/cover/embedding enrichers), first-party or
  deployment-authored, not an anonymous third-party ecosystem. Dynamic loading
  (subprocess/gRPC or WASM) buys nothing an MVP needs and adds dependency surface
  against the "few dependencies" principle (2).
- **Transport boundary.** The `Provider` interface is defined so a subprocess or
  WASM *transport* could implement it later -- if non-Go authors or untrusted
  third-party providers (multi-tenant SaaS Tier 2, roadmap Phase 5) ever become
  real -- without rewriting providers. Pay for the interface now, the loader only
  if needed. See `tasks/006`.

## 10. Theming / implementation model

A deployment (e.g. qllpoc):

- depends on `github.com/freeeve/libcatalog` (Go) and the Hugo module;
- supplies **config** (providers, enabled vocabularies, feature flags such as
  embeddings on/off, languages);
- supplies a **theme** (Hugo theme/overrides on top of the module's templates
  and assets);
- adds **local extensions** (its own predicates, custom facets, custom pages);
- optionally runs **Tier 2** for collaborative cataloging.

qllpoc is the reference implementation and proving ground.

## 11. Guardrails & non-goals

- Availability/circulation is **not** modeled in the graph (live, client-side),
  and therefore **Tier 1 does not facet or sort by availability** -- "available
  now" is live per-view only. A coarse, periodically-refreshed indexable
  availability sidecar, if ever wanted, is a Tier-2 opt-in.
- No triplestore, no SPARQL endpoint, no per-record database -- git + object
  storage only.
- Canonical, sorted, skolemized RDF, or the git/audit story breaks.
- **Not an ILS.** No acquisitions, no patron accounts, no lending -- borrowing is
  the provider's job (OverDrive, etc.). libcatalog is discovery + cataloging: the
  bibliographic half only.
- Embeddings / paid AI never on by default.

## 12. Component / dependency map

Sibling repos under one parent directory:

- `libcatalog/` -- this framework.
- `libcodex/` (`github.com/freeeve/libcodex`) -- MARC/MODS/DC/schema.org/BIBFRAME
  read-write-convert + RDF toolkit + streaming authority-file decoder.
- `roaringrange/{go,rust,python}` (`github.com/freeeve/roaringrange`) -- search
  index + reader. `terms` = lexical/BM25 (default); python/model2vec =
  embeddings (opt-in).
- deployments (e.g. `qllpoc/`) -- implement the framework.

## 13. Proposed repo layout

```
libcatalog/
  README.md
  docs/            ARCHITECTURE.md, ROADMAP.md
  go.mod           module github.com/freeeve/libcatalog
  cmd/lcat/        the projector / import-export CLI
  bibframe/        record <-> BIBFRAME crosswalk (over libcodex)
  identity/        two-tier Work/Instance ids + clustering
  ingest/          provider interface; overdrive + marc providers
  project/         graph -> catalog JSON + search index
  search/          roaringrange wiring (lexical default; embeddings flag)
  hugo/            the Hugo module: content adapter, layouts, partials, assets
  backend/         (Tier 2) cataloging API/app -- later
```
