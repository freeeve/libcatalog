# Public authority sources

The backend ships a registry of public authority sources (tasks/067,
`backend/vocabsrc`). A source offers **live typeahead** (the term picker and
enrichment reconcile against its public API, proxied server-side), a
**downloadable snapshot** (its SKOS dump installs into the local vocab index
for instant offline search), or both. The Vocabularies screen lists them with
license and install state; Download/Refresh/Remove need the admin role.

## Built-ins

| Source | Scheme | Live | Snapshot | License |
|---|---|---|---|---|
| `lcsh` -- LC Subject Headings | `lcsh` | suggest2 | yes (~450k concepts) | free of known restrictions |
| `lcgft` -- LC Genre/Form Terms | `lcgft` | suggest2 | yes | free of known restrictions |
| `lcshac` -- LC Children's Subjects | `lcshac` | suggest2 | yes | free of known restrictions |
| `lcnaf` -- LC Name Authority File | `lcnaf` | suggest2 | no (~11M concepts -- live only) | free of known restrictions |
| `wikidata` | `wikidata` | wbsearchentities | no | CC0 |
| `viaf` | `viaf` | AutoSuggest | no | ODC-BY |

VIAF hits carry cluster siblings (LCNAF, GND, Wikidata) as `exactMatch` URIs;
a local authority created from such a pick records them as
`skos:exactMatch` cross-references.

The id.loc.gov bulk-download URLs occasionally move; override a built-in by
POSTing a source of the same name with the corrected `snapshotUrl` (deleting
the override restores the shipped definition).

## Snapshot format

A downloadable source's `snapshotUrl` must serve SKOS as **N-Triples or
N-Quads**, optionally gzipped. The converter keeps only the predicates the
index reads (prefLabel, altLabel, definition, broader/narrower/related,
exactMatch, rdfs:label), tags everything into the `authority:<scheme>` named
graph, and writes one snapshot file under the authorities tree
(`data/authorities/vocab/<name>.nq` plus a `.json` sidecar recording scheme,
term count, and install time). The index reload is an atomic snapshot swap;
install state survives restarts because it lives in the blob store.

## Drop-in sources

Register additional sources at runtime -- no code:

```
POST /v1/vocabsources          (admin)
DELETE /v1/vocabsources/{name} (admin)
```

Examples (verify current dump URLs against each project's downloads page):

```json
{"name": "homosaurus", "scheme": "homosaurus",
 "license": "CC-BY-4.0", "homepage": "https://homosaurus.org",
 "snapshotUrl": "https://homosaurus.org/v5.nt"}
```

```json
{"name": "gnd", "scheme": "gnd",
 "license": "CC0", "homepage": "https://lobid.org/gnd",
 "snapshotUrl": "https://data.dnb.de/opendata/authorities-gnd_lds.nt.gz"}
```

```json
{"name": "aat", "scheme": "aat",
 "license": "ODC-BY", "homepage": "https://www.getty.edu/research/tools/vocabularies/aat/",
 "snapshotUrl": "http://vocab.getty.edu/dataset/aat/explicit.zip"}
```

Getty and MeSH publish several serializations; pick an N-Triples export
(the converter does not read zip or Turtle). A source whose API speaks one of
the implemented suggest dialects can also set `suggestFlavor`
(`suggest2` | `wikidata` | `viaf`) with `suggestUrl`/`suggestDataset` for
live typeahead.

## Uploading a dump by hand

When a publisher's download URL is unreachable (or a source has none -- name
and scheme alone are a valid registration), an admin can install a local dump
from the Vocabularies screen, or:

```
PUT /v1/vocabsources/{name}/snapshot   (admin; body = the raw dump bytes)
```

Same format rules as `snapshotUrl` (SKOS N-Triples/N-Quads, optionally
gzipped; zip archives and XML exports are rejected by name). The install is
synchronous and in-memory, so uploads are size-capped: **512MB by default**,
raised per deployment with `LCATD_VOCAB_UPLOAD_CAP_MB`. Gzip compresses
N-Triples roughly 10x, so prefer `.nt.gz`/`.nq.gz` for anything large. The
sidecar records `upload` as the snapshot provenance.

## Enrichment

Every suggest-capable source registers as a moderated enrichment target at
boot (queue mode: candidates land in the review queue, never auto-write).
`POST /v1/enrich/{source}/run` (admin) reconciles the corpus's uncontrolled
tags against it. The legacy `LCATD_ENRICH_LOCSH=queue|direct` variable still
controls the original locsh reconciler and is the only direct-mode path.
Sources registered after boot join the enrichment list at the next restart.

The `crosswalk-<scheme>` sources resolve each work subject's cross-scheme
equivalents into their target scheme: direct skos exact/close links in
either direction AND one-hop pivots through a shared intermediate URI --
the FAST -> LCSH <- Homosaurus shape, where neither LCSH nor any direct
FAST-Homosaurus edge is loaded. Pivot suggestions carry lower confidence
(exact 1.0, close 0.85, pivot-exact 0.8, pivot-close 0.6); the weakest hop
grades the chain, and each candidate keeps its OWN confidence in the queue
(one suggestion batch per tier), so a guard demotion is visible next to its
matched counterpart.

Match links are not transitive, so pivots are guarded against the broad-
heading trap (a node like LCSH "Women" collects both its true counterpart
and narrower identity terms): per pivot node and scheme, a PREFERRED-label-
matching counterpart keeps full pivot strength (a match that exists only
through an alt label -- FAST folds "Womyn" into "Women" as a variant access
string -- is see-also grade and caps at pivot-close), a claimant whose sibling on the same
node is its skos:broader ancestor is dropped outright ("Women" never
suggests "Womyn"), and when several claimants remain the node is treated as
a hub -- non-matching survivors demote one tier (a demoted pivot-close is
dropped as coincidence-grade). The guards run in both directions: when the
SOURCE is the narrow end (its own broader claims the same node -- "Womyn"
asserting exactMatch on LCSH "Women"), nothing non-matching pivots through
at all, and a node with three or more distinct claimants across every
loaded scheme counts as a hub even where each scheme holds just one.

Sparse loads get their own rule, because two loaded schemes starve fan-in
and sibling evidence entirely: when the candidate's own scheme holds a
label counterpart for the source that does NOT claim the pivot node, the
lone divergent claimant drops -- the target vocabulary itself declines the
equivalence the pivot asserts (Homosaurus holds "Women"; a sole-claimant
"Womyn" on the bare LCSH node never suggests). A counterpart co-claiming
the node keeps its adjacent siblings reviewable one tier down. For a FAST-cataloged collection the pivot's first hop is
the FAST term's own LCSH source edge: `lcat vocab-subset` harvests it
automatically for FAST-namespace subjects (the per-term linked data's
`schema:sameAs` to `id.loc.gov`, emitted as `skos:exactMatch`) -- regenerate
an older label-only FAST snapshot to pick the edges up.

### External work identities (OpenLibrary, tasks/066)

`LCATD_ENRICH_OPENLIBRARY=direct` plus `LCATD_ENRICH_OPENLIBRARY_DUMP=<path>`
registers the OpenLibrary source. At boot it builds an ISBN -> OpenLibrary work
index from the offline editions dump (public bulk TSV; no live API on the
ingest path), then a run links each Work whose ISBNs resolve to exactly one
OpenLibrary work with `<work> owl:sameAs <openlibrary URI>` in the
`enrichment:openlibrary` graph -- so the link rides into N-Quads/JSON-LD
exports and, since libcodex v0.29.0, into MARC as an **758** Resource Identifier
with the hub URI in `$1` (tasks/359). A Work whose ISBNs map to conflicting works is left unlinked (never
guessed); the minted `w…` id stays primary. Direct mode is the meaningful one:
an exact ISBN match is deterministic, and the queue path moderates subject
candidates, not identity links.

### SRU/Z39.50 subject harvest (sru-subjects)

When copy cataloging is configured, the `sru-subjects` source registers
automatically: `POST /v1/enrich/sru-subjects/run?filter=k=v` (or the async
`/jobs` variant) asks the copycat targets what the scoped works are about --
ISBN searches, 6XX extraction only (titles/contributors never travel), and
ONLY headings that reconcile to a loaded vocabulary come back, as moderated
suggestions. Identifier matches (`$0`) queue at higher confidence than
whole-heading label matches; a work's existing subjects and tags never
re-suggest; works without an ISBN are skipped. Quota hygiene: at most two
ISBNs per work fan out, with a politeness pause between works -- scope runs
with `?filter` rather than sweeping a large corpus. This complements the
local crosswalk sources: they walk links the loaded vocabularies already
hold, while the harvest discovers headings other libraries assigned (e.g.
Homosaurus 650s at targets that carry them).

### BiblioCommons peer-library subject harvest (bibliocommons)

`LCATD_ENRICH_BIBLIOCOMMONS=<subdomain>[,<subdomain>...]` (e.g. `ccslib`
for the CCS consortium, an early Homosaurus adopter, or a list like
`ccslib,seattle,sfpl`) registers a harvest of peer libraries' public
BiblioCommons OPACs. A job may override the peer list per run --
`POST /v1/enrich/bibliocommons/jobs?hosts=seattle,kcls` -- so one operator
sweeps any subset of the ~20 known Homosaurus-cataloging BiblioCommons
systems without a restart. The direction is deliberately reversed:
a BiblioCommons record page exposes no subjects to an unauthenticated
reader, but the public RSS search
(`/search/rss?q=<term>&t=subject`) feeds every title a subject is assigned
to, 100 items per page. So the source drives one subject search per term of
a loaded vocabulary (`LCATD_ENRICH_BIBLIOCOMMONS_SCHEME`, default
`homosaurus`; retired terms skipped), matches the feed items back to the
scoped works -- by ISBN from the item's Syndetics cover URL first
(confidence 0.9), by normalized title **plus author agreement** second
(0.75; a bare title match on generic titles is measured noise) -- and
queues the DRIVER term on every matched work that does not already carry
it. Queue-only: another library's cataloging is a candidate, not an
assertion.

Several hosts turn the run into a consensus vote: the same term matched to
the same work from N peers files ONE queue row whose supporter count is N
and whose source note names the corroborating libraries ("via kcls,
seattle, sfpl" on the queue row) -- so peer-consensus terms rank above
singletons in the support-ordered queue, at the strongest match tier any
peer earned. A re-run refreshes an open machine row's census in place;
patron-backed and already-reviewed rows are never touched.

The inherent bound: the harvest can only confirm terms it queries, never
reveal one it did not -- coverage equals the driver vocabulary. Politeness
and cost: requests to one host pause 1.5s apart (distinct hosts crawl
concurrently, capped at four by default --
`LCATD_ENRICH_BIBLIOCOMMONS_CONCURRENCY` raises it for wide consensus
runs; politeness stays per host) and each term stops at
`LCATD_ENRICH_BIBLIOCOMMONS_MAX_PAGES` (default 6, i.e. 600 items; large
terms are truncated and logged). A completed crawl is reused per host for 24
hours, so re-running against a different `?filter` scope -- or a different
host list overlapping a warm host -- re-matches without touching that peer
again. The feed's OCLC numbers are parsed but not
yet matched (work summaries do not carry OCLC identifiers).

### III Vega Discover peer harvest (vega)

`LCATD_ENRICH_VEGA=<siteCode>.<region>[,<siteCode>.<region>...]` (e.g.
`nypl.na2,mdpls.na` -- each library's Vega catalog subdomain and region,
read off its catalog URL `https://<siteCode>.<region>.iiivega.com/`)
registers a harvest of III Vega Discover catalogs -- the systems
BiblioCommons cannot reach (NYPL among them). Unlike the RSS harvest's
search-term inference, Vega's concept model states its vocabulary
outright: each driver term resolves (suggestions -> concept, gated on the
concept's `source=homoit`) to the region's shared concept UUID, whose
cataloged FormatGroups then page in with typed identifiers -- so a match
is a peer's EXPLICIT Homosaurus assertion joined by a shared ISBN
(confidence 0.9; there is no title-fallback tier). Queue-only, moderated,
with the same consensus semantics as the BiblioCommons harvest: N
corroborating tenants endorse one suggestion, each with a verifiable
record link.

Mechanics: concept resolution is cached per REGION (every tenant in `na`
shares one UUID per label), so multi-tenant runs resolve each label once;
politeness is per region host (all of a region's traffic hits
`<region>.iiivega.com`), requests 1.5s apart, `LCATD_ENRICH_VEGA_MAX_PAGES`
capping pages per concept (default 6 x 96 records). Harvests cache 24h per
tenant. The siteCode must be the library's real subdomain -- DNS is a
wildcard, so nothing validates a guess until the API answers 403.

### TLC LS2 PAC peer harvest (tlc)

`LCATD_ENRICH_TLC=<host>[,<host>...]` -- each entry is either a bare
subdomain of `<tenant>.tlcdelivers.com` (e.g. `nbpl`) or a full vanity
catalog host (e.g. `ls2pac.lapl.org`; many TLC libraries, LAPL among them,
serve the same LS2 PAC API on their own domain). A token with a dot is used
verbatim, one without expands to `<token>.tlcdelivers.com`. It registers a
harvest of TLC LS2 PAC catalogs: one anonymous faceted search per driver term per
tenant, the term serving as BOTH the keyword and a Subject facet filter so
the facet enforces subject-cataloging precision. The subject index is
unscoped (LCSH and Homosaurus merge, records carry no scheme tag), so like
the BiblioCommons harvest this is the inference model -- the match is the
exact Homosaurus prefLabel -- joined by a shared ISBN (probed records
carried ISBNs universally; confidence 0.9, one tier). Queue-only, with the
same consensus semantics, per-job `?hosts=` override, 24h per-tenant
cache, per-tenant 1.5s politeness, and `LCATD_ENRICH_TLC_MAX_PAGES`
(default 6 x 24 hits) as the other peer harvests.

### SirsiDynix Enterprise peer harvest (sirsidynix)

`LCATD_ENRICH_SIRSIDYNIX=<host>[/<profile>][,...]` registers a harvest of
SirsiDynix Enterprise catalogs. Each entry is a tenant: a bare subdomain
(e.g. `winca`) expands to `<host>.ent.sirsidynix.net`, a host with a dot is
used verbatim (for the sites that front the catalog on a custom domain),
and the profile defaults to `default`. Per tenant, one anonymous
Subject-scoped RSS hitlist runs per driver term
(`/client/rss/hitlist/<profile>/qu=<label>&rt=false|||SUBJECT|||Subject`),
returning the whole result set in a single request (no pagination). The
subject index is unscoped, so like the BiblioCommons and TLC harvests this
is the inference model -- the match is the exact Homosaurus prefLabel --
joined by a shared ISBN pulled from each entry's content block (nearly
universal in probed catalogs; confidence 0.9, one tier). Each endorsement
carries the record's public detail link as verifiable evidence. Queue-only,
with the same consensus semantics, 24h per-tenant cache, and per-tenant
1.5s politeness as the other peer harvests. Some tenants front the catalog
with a Cloudflare challenge; those answer with an HTML interstitial instead
of an Atom feed and are detected and skipped (the skip counted), not
harvested as empty.

## Harvest languages

The inference harvests (bibliocommons, tlc, sirsidynix) drive their searches
from the loaded vocabulary's prefLabels, English by default.
`LCATD_ENRICH_LANGS` (comma-separated, default `en`) adds other label
languages: set `en,es` and each concept is also searched by its Spanish
label where one exists, reaching peers that catalog LGBTQ material in
Spanish (the Chilean/LatAm SirsiDynix cluster, Miami-Dade, San Diego, ...).
A Spanish-string match still queues the concept's URI -- the same
inference-model candidate as the English path, moderated in the queue. Terms
with no label in a configured language are skipped for that language, and a
concept whose labels coincide across languages is not searched twice. Vega
is English-only regardless: it resolves each label to an EXPLICIT Homosaurus
concept (`source=homoit`), and those concepts are English-labeled, so a
Spanish query would resolve to a non-homoit concept and be gated out.

## Unreachable-host circuit break

All four peer harvests fail fast on a misconfigured host. A per-term miss (a
404, no concept in a region, an empty result) is normal and the run
continues, but a host that does not resolve, refuses the connection, or
times out is a configuration error: after a bounded number of CONSECUTIVE
connection-class failures the harvest aborts and the job goes FAILED with
`peer unreachable: <host>`, naming the offending entry. So a typo in a
multi-host list surfaces in seconds -- the operator is told which host --
instead of grinding every driver term (hours) to produce nothing. The
per-request timeout bounds one request; this bounds a run where every
request fails.

## Scheme filtering

`LCATD_VOCAB_SCHEMES` (when set) filters which authority graphs load.
Installed snapshots widen the effective filter automatically -- an authority
edit's index reload never drops an installed vocabulary.
