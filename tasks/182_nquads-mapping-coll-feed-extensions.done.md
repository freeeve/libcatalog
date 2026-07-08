# 182 -- nquads provider: mapping extensions for the coll-support full-corpus feed

## Done (2026-07-08)

All five gaps closed, mapping-driven and backward compatible (an existing
mapping TOML behaves exactly as before):

1. **Bucket grouping**: a mapped `group` field (dcterms:isPartOf-shaped;
   self when absent) namespaces the computed author key by the GROUP id, so
   a cluster's format buckets share one Work with one Instance each while
   distinct clusters never re-merge. Provider ids stay per-record
   (`<id-scheme>:<workid>`), round-tripping the old coll keys. New
   `identity-language` fixes the resolution-key language (identity.WorkKey
   folds lang; the old provider keyed everything under eng) so fresh mints
   match too.
2. **Extras passthrough**: `extras-prefix` harvests predicate-prefix
   statements verbatim (first wins a key); the [sources] mechanism keeps
   owning its extra key on a collision.
3. **New mapped fields**: subtitle, summary, contributor ("Last, First
   (role)" literals -- first primary, role lowercased, default author;
   creator stays the identity access point and the fallback), publisher +
   issued (Publication provision), format (physical|ebook|audiobook -> RDA
   337 s/c/n), tag (Topic, no source, before keywords), keyword (Topic,
   `keyword-source`), classification ([classifications] prefix/source;
   Label from the code IRI's harvested prefLabel). Languages now carry ALL
   statements in order (was first-only).
4. **Identifier rules**: the legacy string form is unchanged; the table
   form ({class, source, key}) covers non-key identifiers -- isbn10 as a
   display Isbn, asin/odreserve as raw source-tagged values.
5. **Broader + standalone terms**: prefLabel harvesting went multi-language
   (untagged = en per the contract), broader edges harvest alongside;
   ControlledSubjects carry Labels-per-language + Broader, and
   DescribedTerms walks the transitive closure (BFS, depth 12, cycle-safe,
   direct subjects excluded, undescribed URIs skipped, sorted) into the
   feed graph via 180's TermDescriber -- feeding the v10 terms sideband.

Verified: unit tests over a coll-shaped fixture (grouped identity, key-ness,
provisions, media, roles, topics order, classification labels, term walk)
plus a Run-level test (three buckets -> one grain, ancestor described, no
subject link); full-corpus run against the real coll-support export --
62,602 works / 77,084 instances (exactly the contract), projection carries a
10,050-term labeled sideband (homoit0000915/0001297 resolve to "LGBTQ+
people" / "Sexual minorities" -- the 176 URI-roots), and works/subtitle
counts match the deployed old-provider catalog.json exactly (62,602 /
11,885). Gotcha for the adopting mapping: the export writes
`http://schema.org/...` (not https) for alternativeHeadline/keywords.
docs/build-pipeline.md documents the full surface.

---

Original note follows.

Left by the coll-support session 2026-07-08 (uncommitted cross-repo ask;
companion to queerbooks-demo tasks/028 "de-Go" and their new tasks/029).
Eve OK'd reshaping the nquads implementation for this.

coll-support now emits a full-corpus export (`collctl export collnq` ->
catalog.coll.nq, 62,602 clusters / 77,084 records / 1.72M quads) meant to
replace queerbooks-demo's bespoke coll.db Go provider through the stock
`nquads` provider with `feed = "coll"`. The emitted contract is documented in
coll-support `docs/coll-feed-nq.md`; the reference semantics are queerbooks'
`ingest/coll` (which coll-support ported 1:1). Today's provider
(ingest/nquads, v0.30-v0.34) consumes only the 7 mapped fields, one record +
one Instance per work IRI -- the gap list, in rough priority order:

1. **Per-format Instance grouping.** The export has one record per
   cluster-format bucket: `urn:coll:work:<id>` (formatless) and
   `urn:coll:work:<id>:<format>`. Records sharing a grouping key --
   `dcterms:isPartOf <urn:coll:work:<id>>`, self when absent -- must group
   into ONE Work with one Instance each (namespace the computed author key
   by the group id, not the full workid, mirroring ingest/coll record.go
   Identity). Provider ids stay per-record (`coll:<id>[:<format>]`), which
   round-trips the old provider keys byte-for-byte.
2. **Extras passthrough.** `urn:coll:extra:<key> "value"` -> Extras()[key],
   verbatim. This carries the sources extra with RAW names ("QLL,loc,…"),
   so the public-sources allowlist keeps governing the same key; the
   [sources] graph/slug mechanism is not used by this feed.
3. **New mapped fields** (all in docs/coll-feed-nq.md): subtitle
   (schema:alternativeHeadline -> Title.Subtitle), summary (dcterms:abstract),
   contributor (`"Last, First (role)"` literals -> Contributions with real
   roles, first primary; creator stays the identity access point), publisher
   + issued (-> Provision Publication), format literal (physical|ebook|
   audiobook -> RDA media 337 s/c/n), keywords (schema:keywords -> Work
   Subject Topic source "overdrive"), qll-tag (urn:coll:p:qll-tag -> Topic,
   no source, ordered before keywords), classification
   (urn:coll:p:classification -> urn:bisac:<code>, Value=code Label=the
   code's skos:prefLabel Source=bisacsh).
4. **Identifier scheme config**: per-prefix class/source and key-ness --
   urn:coll:isbn10: (class Isbn, NOT a merge key), urn:coll:asin: (source
   asin, non-key), urn:coll:odreserve: (source overdrive-reserve, non-key,
   held digital buckets). urn:isbn: stays the cross-feed key.
5. **skos:broader + standalone terms.** The export asserts used terms'
   broader edges and ancestor concepts' own prefLabel/broader statements
   (depth-capped 12). ControlledSubjects should carry Broader, and the
   standalone descriptions need a provider-side terms path into the sideband
   -- the capability 180 already sketches; sequencing with the 180 Merge fix
   makes queerbooks' patch-terms shim fully retirable.

Notes: untitled works are currently dropped by the provider (provider.go
~:159) -- the corpus has zero today, fine to keep; statement order per
predicate must be preserved (contribution/language order is meaningful);
en prefLabels ride untagged, other languages @lang-tagged.

Validation model: their tasks/024 (statement-identical grains vs the old
provider); queerbooks tasks/029 runs the WorkID-parity + leak-scan pass
before flipping lcat.toml.
