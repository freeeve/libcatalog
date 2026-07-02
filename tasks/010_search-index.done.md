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
      (now with a **BM25 impact sidecar** per language + **all-18 Snowball stemming**
      + a **trigram (RRSI) arm** for unsegmented scripts, manifest v3 -- see below).
- [x] Browser search answers queries over the built site. **Delivered via `tasks/017`
      (Pagefind default)** -- per-language, CJK-capable, faceted, no bespoke WASM wiring.
      The roaringrange WASM reader over this manifest is now the opt-in **advanced**
      search path, no longer the default-ship gate. The build side below is complete;
      the WASM reader remains tracked as advanced-path work (`tasks/009`).
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

3. **Trigram (`RRSI`) arm now wired** (delivered under `tasks/005`, done): unsegmented
   -script languages route to a `.rrs` trigram index instead of a word-level `.rrt`.
   The manifest gained `kind` ("terms"|"trigram") + `gramSize`, advancing **SchemaVersion
   2 -> 3**. No CJK in the current eng/spa corpus, so every index there stays
   `kind:terms`; recall is proven by a Go-reader unit test (`TestTrigramRecall`).

Manifest (v3) carries `version` + per-index `kind` + tokenizer flags + `impacts`/`gramSize`
so the WASM reader picks the query path (term tokenizer vs `NgramKeys`), tokenizes
identically, and loads the BM25 sidecar.

## Closeout (build side done; reader -> advanced path)

The build-side deliverable is complete and committed: `lcat index` emits per-language
term (`.rrt`) + BM25 impact (`.rrb`) + trigram (`.rrs`) indexes and the v3 routing
manifest, validated on the 5,659-Work corpus (`go test ./search` green at commit). The
one open acceptance item -- the browser WASM reader -- was superseded as a *default*
requirement by `tasks/017` (Pagefind is the default browser search; roaringrange is the
opt-in advanced engine), so this task is closed for its build-side scope with the reader
tracked as advanced-path work (`tasks/009`).

Note: `go test ./search` cannot be re-run in the current working tree because the sibling
`../libcodex` checkout is mid-refactor (uncommitted contribution/role changes) and does
not compile through the local `replace`. That is an external concurrent edit, not a
libcatalog regression -- the search build side is unchanged here and was green when
committed. Re-validate once libcodex settles.
