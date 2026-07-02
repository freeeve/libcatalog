# 010 -- Search index: roaringrange wiring (lexical, per-language)

## Context

With `catalog.json` available (`lcat project`), build the search index the Hugo
module's browser reader queries (ARCHITECTURE §8). Parallelizable with `tasks/009`.

## Scope

A `search/` package wiring roaringrange (`github.com/freeeve/roaringrange`):

1. **Lexical / BM25 default** -- the `terms` path; no embeddings, no paid AI in
   the default build. The vector arm stays a build flag, off by default.
2. **Per-language stemmed indexes** -- an `RRTI` index carries one stemmer
   language, so build one stemmed index per corpus language, routed by each Work's
   `languages` (from catalog.json / MARC 041). Emit the small **language->index
   map** the query side uses. The 18 Snowball languages get stemmed indexes;
   unsupported languages get an unstemmed word-level index; unsegmented scripts
   (CJK/Thai/...) route to the trigram (`RRS`) index; no language signal -> the
   unstemmed fallback.
3. **Go-side stemmer wiring** -- the Go projector currently wires only English;
   the other 17 Snowball languages need Go-side wiring (or building via the Rust
   builder). Coordinate with roaringrange `tasks/055` (language-keyed stop words)
   and the Snowball coverage note in §8.
4. **Fields** -- index title, contributors, subjects (incl. curated/editorial);
   store the Work id for result -> page linking.

## Acceptance

- [ ] `lcat` builds per-language indexes + the routing map from the projected data.
- [ ] The roaringrange WASM reader answers a query in the browser over the emitted
      index (smoke test in the Hugo module, `tasks/009`).
- [ ] Embeddings remain opt-in (build flag), off by default.
