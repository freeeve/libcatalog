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

- [x] `lcat` builds per-language indexes + the routing map from the projected data
      (now with a **BM25 impact sidecar** per language + **all-18 Snowball stemming**,
      manifest v2 -- see "v2 shipped" below).
- [ ] The roaringrange WASM reader answers a query in the browser over the emitted
      index (smoke test in the Hugo module, `tasks/009`). **Remaining gate** -- the
      build side is complete; this is browser/WASM front-end wiring.
- [x] Embeddings remain opt-in (build flag), off by default -- lexical only; no
      vector arm is wired.

## Build notes (v1 shipped)

The `search/` package builds `lcat index --catalog catalog.json --out <dir>`:
one `term-<lang>.rrt` (roaringrange `WriteTermIndexFull`) + `term-<lang>.docs.json`
(dense doc-id -> Work id) per corpus language, plus `search-manifest.json` routing
language -> index with the tokenizer settings (termLanguage, stemmed, stopwords) so
the reader tokenizes queries identically. Indexed text: title, subtitle, contributor
names, subject labels. Doc ids are dense from 0 in projected (sorted) order.

Validated on the corpus: 5659 Works -> `eng(5286)` + `spa(373)` indexes (488K/79K
`.rrt`); no `und` index (every Work carries a language). `go test ./search` passes.

### v2 shipped -- BM25 + all-language stemming (roaringrange `tasks/073` landed, v0.27.0)

The two Go-build-side roaringrange gaps that pinned v1 to a degraded index closed in
roaringrange **v0.27.0** (its `tasks/073`, done), and libcatalog now consumes them:

1. **BM25 impact sidecar (was: plain boolean).** `search/search.go` now builds each
   `.rrt` via `WriteTermIndexFullDict` (returns the dict with the real posting
   head-offsets) and writes a paired `term-<lang>.rrb` (RRSB) via `WriteImpacts` with
   `NewImpactsAccumulator` (per-doc length + term frequency) at roaringrange's default
   k1/b. The `.rrt` stays a presence index; BM25 tf lives in the sidecar. Manifest
   gains `impacts` per index; **SchemaVersion 1 -> 2**.
2. **All-18 Snowball stemming (was: English-only).** `NewTermTokenizerFull` now stems
   any mapped language Go-side, so `termLanguage` returns `stem = tl != None`. `spa`
   now builds a stemmed index (byte-exact vs the Rust reader, guaranteed by
   roaringrange's `TestTokenizerStemMatchesRustGolden`).

Re-validated on the corpus: 5659 Works -> `eng(5286)` + `spa(373)`; each emits a
`.rrt` + `.rrb` (RRSB magic verified), `spa stemmed:true`, manifest **v2**; re-index
is **byte-identical** (deterministic). `go test ./search` passes.

3. **Still no trigram (`RRS`) arm** for unsegmented scripts (CJK/Thai) -- **not** part
   of `073`; those still fall to word-level. Remains future work (see `tasks/005`); no
   CJK in the current corpus so it is unexercised.

Manifest carries `version` + per-index tokenizer flags + `impacts` so the WASM reader
tokenizes queries identically and loads the sidecar.
