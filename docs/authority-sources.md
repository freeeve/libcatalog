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
(exact 1.0, close 0.85, pivot-exact 0.8, pivot-close 0.7); the weakest hop
grades the chain.

Match links are not transitive, so pivots are guarded against the broad-
heading trap (a node like LCSH "Women" collects both its true counterpart
and narrower identity terms): per pivot node and scheme, a label-matching
counterpart keeps full pivot strength, a claimant whose sibling on the same
node is its skos:broader ancestor is dropped outright ("Women" never
suggests "Womyn"), and when several claimants remain the node is treated as
a hub -- non-matching survivors demote one tier (a demoted pivot-close is
dropped as coincidence-grade). For a FAST-cataloged collection the pivot's first hop is
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

## Scheme filtering

`LCATD_VOCAB_SCHEMES` (when set) filters which authority graphs load.
Installed snapshots widen the effective filter automatically -- an authority
edit's index reload never drops an installed vocabulary.
