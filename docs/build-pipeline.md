# The config-driven build pipeline (`lcat build`)

Adopters should not need Go (tasks/172). A deployment describes its whole
static-site build in one `lcat.toml`, and `lcat build` drives it:

```
ingest (every [[source]]) -> serialize -> project -> export -> index -> hugo
```

Steps run only when their section is present in the config; `--only
step[,step]` narrows a run while iterating (e.g. `lcat build --only
project,index` after an editorial merge).

## lcat.toml

```toml
# Grain root every step shares: catalog.nq + data/works/ live here.
out = "data/out"

# One [[source]] per ingest feed, in priority order -- the first listed
# feed wins a shared work at projection time.
[[source]]
provider = "marc"            # registry name: overdrive, marc, nquads, csv, ...
source = "exports/full.mrc"  # provider input
# feed = "sierra"            # provenance graph override (default: provider name)
# reconcile = "review"       # flag feed-only works the scan no longer lists
# reconcile-allow-empty = false

[[source]]
provider = "nquads"
source = "exports/community.nq"
mapping = "community-mapping.toml"   # shorthand for params.mapping
# [source.params]                    # any provider parameter
# tentative = "drop"

[project]
out = "site/assets"                  # catalog.json + facets.json + redirects.json + similar.json
# providers = ["marc", "nquads"]     # default: each source's feed in order
public-sources = ["loc", "QLL"]      # extra.sources allowlist for the public face
# subject-schemes = ["https://homosaurus.org/v5/=homoit"]
# similar = 8                        # "more like this" neighbours per work; 0 removes the sidecar

[export]
out = "site/static/downloads"        # catalog.nq.gz + catalog.mrc.gz + catalog.xml.gz
manifest = "site/data/downloads.json"
# public-sources = [...]             # default: inherits [project] public-sources

[index]
out = "site/static/search"           # roaringrange search + browse artifacts

[hugo]
dir = "site"
# command = ["hugo", "--minify"]     # default ["hugo"]
```

The same steps remain available as individual verbs (`lcat ingest`, `lcat
serialize`, `lcat project`, `lcat export`, `lcat index`) with matching flags;
the config file is the orchestration layer, not a different code path.

### catalog.nq has more than one writer

Ingest writes `catalog.nq` alongside the grains; `serialize` regenerates it from
the committed grains, which is what a multi-source deployment needs (after
several ingests, ingest's copy holds only the last run's works). Both emit the
**same bytes** for the same grains: each grain's own canonical N-Quads, with its
blank-node labels namespaced by work id (tasks/291, tasks/298).

That matters because `export` gzips whichever `catalog.nq` it finds. Until
v0.121.2 the two writers disagreed -- ingest re-encoded the graphs and emitted
traversal-order `_:b1, _:b2, …` labels -- so a pipeline that ran ingest without
serialize published a 60MB download whose sha256 moved on every rebuild of an
unchanged catalog. Running `serialize` after a single-provider ingest is now a
byte-for-byte no-op, and `export` warns if it finds a `catalog.nq` an older
`lcat` left behind.

### Public provenance allowlist

`public-sources` strips `lcat:extra/sources` attributions not in the allowlist
from **both** public surfaces -- the projected `catalog.json` and the
`catalog.nq.gz` download -- so community-source attribution never leaks
further than the deployment intends. The on-disk graph of record stays
complete; curators still see full provenance in the backend. An empty/absent
list keeps everything.

### Multi-feed projection

The projector views one provenance graph at a time, so each feed projects
separately and the catalogs merge by work id, first-listed feed winning a
shared work (list the richest feed first). Works shared across feeds cluster
at ingest time through identifier keys (ISBNs, or the mapped id schemes), so
"the same book from two sources" is one work with both feeds' provenance.

## Generic providers: sideload with a mapping, not Go

Three providers cover the common sideload shapes with no code (precedent:
Aspen Discovery side loads -- librarians load exports with an indexing
profile):

- **`marc`** -- ISO 2709 file, no mapping needed (the crosswalk is libcodex's).
- **`nquads`** -- a dcterms-shaped RDF export, driven by a TOML mapping.
- **`csv`** -- a spreadsheet export, driven by a TOML mapping.

The Go `ingest.Provider` seam stays for genuinely bespoke sources (a
deployment's own database, a vendor API); it is the exception, not the
on-ramp (ARCHITECTURE §9a).

### nquads mapping

```toml
work-prefix = "urn:coll:work:"   # subjects under this prefix are works
# id-scheme = "collnq"           # durable id namespace (default: feed name)
# class = "Text"
# default-language = "eng"
# identity-language = "eng"      # fix the resolution-key language (tasks/182):
#                                # matches WorkIDs freshly minted by a replaced
#                                # provider that keyed everything under one lang
# id-order = "numeric"           # or "lexical" (default)
# keyword-source = "overdrive"   # bf:source on "keyword" topics (tags carry none)
# extras-prefix = "urn:coll:extra:"  # predicate PREFIX -> work extras, verbatim

[predicates]                     # field = predicate IRI (or list of IRIs)
title = "http://purl.org/dc/terms/title"
subtitle = "https://schema.org/alternativeHeadline"
creator = "http://purl.org/dc/terms/creator"          # identity access point
contributor = "http://purl.org/dc/terms/contributor"  # "Last, First (role)" literals
summary = "http://purl.org/dc/terms/abstract"
identifier = "http://purl.org/dc/terms/identifier"
subject = "http://purl.org/dc/terms/subject"          # authority IRIs
tag = "urn:coll:p:qll-tag"                            # uncontrolled topics, no source
keyword = "https://schema.org/keywords"               # uncontrolled topics, keyword-source
classification = "urn:coll:p:classification"          # coded IRIs, see [classifications]
source = "http://purl.org/dc/terms/source"
language = "http://purl.org/dc/terms/language"
publisher = "http://purl.org/dc/terms/publisher"      # Instance provision
issued = "http://purl.org/dc/terms/issued"            #   "
format = "http://purl.org/dc/terms/format"            # physical|ebook|audiobook -> RDA 337
group = "http://purl.org/dc/terms/isPartOf"           # bucket grouping, see below
prefLabel = "http://www.w3.org/2004/02/skos/core#prefLabel"
broader = "http://www.w3.org/2004/02/skos/core#broader"

[classifications]
prefix = "urn:bisac:"            # object IRI minus prefix = the code Value;
source = "bisacsh"               # Label = the IRI's harvested prefLabel

[identifiers]                    # object URN prefix = scheme; "isbn" clusters cross-feed
"urn:isbn:" = "isbn"
"urn:overdrive:" = "overdrive"   # string form: always a resolution key
[identifiers."urn:coll:isbn10:"] # table form (tasks/182): non-key identifiers
class = "Isbn"                   # "Isbn" or "Identifier" (default)
key = false                      # rides the Instance, never a merge key
[identifiers."urn:coll:odreserve:"]
source = "overdrive-reserve"     # bf:source tag (raw value, no scheme prefix)
key = false

[languages]                      # export code = ISO 639-2/B
en = "eng"
fr = "fre"

[sources]
prefix = "urn:coll:src:"         # stripped to form source slugs
# extra-key = "sources"          # the extra the public allowlist governs
tentative = ["urn:coll:src:scan-tier-2"]  # attestation that confers no confidence
```

Works attested only by `tentative` sources get the `tentative = "yes"` extra;
`params.tentative = "drop"` drops them at ingest instead.

**Bucket grouping** (tasks/182): records sharing a `group` object (self when
absent) cluster into ONE work with one Instance each -- how a per-format
export (`urn:coll:work:7`, `urn:coll:work:7:ebook`, ...) keeps one work per
cluster. Work-level statements must repeat on every bucket record (first
record wins); identifiers, publisher/issued, and format are per-bucket.

**Term descriptions**: `prefLabel`/`broader` statements on non-work subjects
describe the works' controlled subjects and their ancestor chains (untagged
labels count as English; other languages ride `@lang` tags). Subjects emit
with labels + broader; described ancestors ride along as standalone
feed-graph terms (no subject link) feeding the catalog's vocabulary sideband
(tasks/178/180), so subject trees keep labeled top levels.

### csv mapping

```toml
# id-scheme = "mylib"            # durable id namespace (default: feed name)
# delimiter = ","                # e.g. "\t" for TSV
# multi-separator = ";"          # splits multi-valued cells
# default-language = "eng"

[columns]                        # field = header name; title is required
id = "Record ID"
title = "Title"
subtitle = "Subtitle"
creator = "Authors"
isbn = "ISBNs"
subject = "Genres"               # uncontrolled labels (feed tags)
language = "Lang"
summary = "Description"

[extras]                         # extra key = header name (adopter display fields)
cover = "Cover URL"

[languages]
en = "eng"
```

Map an `id` column whenever the export has a stable record id: it is the
durable key that keeps isbn-less rows from re-minting on every ingest.
