# 284 -- more like this: shared similarity scorer, precomputed for the OPAC and queried live in admin

Opened 2026-07-10, from Eve:

> another feature I want is "more like this" on the detail screen of a particular
> work, that shows similar works based on graph traversal similar to qllpoc. we
> can precalculate it in the build step for opac, query it live for the admin site.

## What qllpoc actually does

Read rather than assumed (`~/qllpoc`). It is **not** an embedding recommender with
a graph bolted on; the graph traversal is the primary signal and the embeddings
are one additive term among several.

**The walk** (`site/assets/js/similar.js:102-150`). For each relation in
`IN_SERIES, BY_AUTHOR, HAS_TAG, HAS_OD_SUBJECT, BY_TRANSLATOR, HAS_KEYWORD`, a
2-hop bipartite co-occurrence walk: focus work -> attribute node -> co-occurring
works. Each shared attribute contributes

```js
var df = works.length;                       // document frequency of the attribute
if (df <= 1 || df > DF_CAP) continue;        // singletons carry no signal; common terms carry no discrimination
var w = weight / Math.log2(df + 2);          // rarity weighting, IDF family
```

**Edge weights**: `IN_SERIES 5, BY_AUTHOR 3, HAS_TAG 2, HAS_OD_SUBJECT 1,
BY_TRANSLATOR 1, HAS_KEYWORD 0.5`. Keywords are deliberately held at 0.5 -- the
code comment says higher weights let one coincidental phrase float unrelated
books in.

**The concept-tree walk** (`similar.js:119-150`) is the part worth stealing. The
focus work's Homosaurus terms are expanded up `skos:broader` for `MAX_DEPTH = 2`
hops with `HOMO_DECAY = 0.5` per hop, plus direct `narrower` children at 0.5.
Each expanded term then does the same 2-hop walk. Two books match if they share a
concept **or a nearby one in the tree** -- which is what makes it feel like
subject cataloging rather than string matching.

**`DF_CAP` is relative**: `Math.floor(0.20 * nWorks)`, recomputed at load, so it
scales with the collection instead of rotting as a constant.

**Fusion**: additive. Same-language is a flat `+20` that deliberately bypasses the
DF cap and only boosts candidates something else already scored; same-age `+10`;
embedding cosine `* 5`; curated-list co-membership `+5` (skipping administrative
lists over 100 members); availability `+0.3`; a fiction-class penalty of `-25`
applied only to embed-only nonfiction candidates on a Spanish fiction rail.

The focus work is excluded at every contribution site (`if (wn !== node)`), and
other editions of the same book are dropped by cluster key at dedup.

Ranking is `sort by score desc`, pool of 200 candidates, revealed 8 at a time to
a hard cap of 48.

**Precomputed vs live**: only the *graph* is precomputed (`catalog.rcpg`, 5.6 MB,
a CSR graph read by a WASM reader). The traversal, weighting, fusion, dedup and
ranking all run **live in the browser** on each detail page. Nothing precomputes a
final ranked list.

That last point matters for Eve's framing: qllpoc's OPAC does not precompute
"similar works per work" either. It precomputes the adjacency and walks it on
demand. We can precompute the final lists, which is simpler, and is the right call
for a Hugo site with no WASM graph reader.

## What libcat already has

Better raw material than expected.

| qllpoc node type | libcat equivalent | where |
|---|---|---|
| `HAS_OD_SUBJECT` | controlled subject IRIs | `ingest.WorkSummary.Subjects` |
| `HAS_TAG` | uncontrolled tags | `WorkSummary.Tags` |
| `BY_AUTHOR` | contributors | `WorkSummary.Contributors` |
| Homosaurus `BROADER` tree | **`vocab.Term.Broader` / `.Narrower`** | `backend/vocab/vocab.go:50-51` |
| `IN_SERIES` | series (schema v11) | projection only |
| language | `language` taxonomy | projection only |
| `HAS_KEYWORD` | -- | none |
| embeddings | -- | none, and out of scope |

`vocab.Index.Resolve` already returns a term's `Broader`/`Narrower`, and
tasks/176 already distinguishes a hierarchy-bearing scheme from a flat one. The
concept-tree walk is available today with no new data.

The gap: **`WorkSummary` carries no series, language or classification**, so the
live admin path would score on subjects + tags + contributors + extras only. The
projection has all three (they are declared taxonomies in `hugo/hugo.toml:14-20`).
Either extend `WorkSummary` (it is what `workindex` holds in memory for every
work -- see the sizing note in `tasks/085`, memory is the wall at 10M works) or
accept a weaker admin ranking than the OPAC's. **Recommend extending it**: series
and language are two short strings and they are the two highest-weighted signals
in qllpoc.

## Design

**One scorer, two callers.** The whole risk in "precompute for OPAC, query live
for admin" is that they drift and the same work gets different neighbours on the
two surfaces -- which is the same class of bug as tasks/253, where the rail and
the query disagreed about what was filtered. So:

- `similar/` in the **root** module: a pure scorer over an in-memory postings
  index. No blob store, no HTTP, no Hugo.
  - `similar.Build(works []ingest.WorkSummary, opts Options) *Index` -- builds the
    inverted index attribute -> works, once.
  - `(*Index).Neighbors(workID string, n int) []Scored` -- the 2-hop walk.
  - `Options` carries the weights, `DFCapFraction` (default 0.20), `TreeDepth`
    (2), `TreeDecay` (0.5), and a `Broader func(iri string) []string` hook so the
    scorer never imports `backend/vocab`.
- `project` (build step) calls it once and emits a `similar.json` sidecar keyed by
  work id, bumping `project.SchemaVersion`. The Hugo `page.html` renders it in a
  new section beside the existing `lcat-relations` block
  (`hugo/layouts/page.html:74`), which holds *asserted* BIBFRAME relations --
  these are *computed* ones and must be visually distinct and labelled as such.
- `backend/httpapi` adds `GET /v1/works/{id}/similar?limit=` (librarian), scoring
  against an index built from `workindex` summaries and rebuilt on the same
  freshness signal the works list uses. `WorkEditor.svelte` renders the panel.

**Exclusions, decided up front:** tombstoned works are never neighbours and never
have neighbours -- retiring a record must not leave it recommended from elsewhere
(cf. tasks/280). Suppressed works stay in the admin index and are absent from the
projection, which already happens naturally since `lcat project` drops them. The
focus work is excluded, and so is any other edition of it if we have a cluster key.

## Open questions for Eve

1. **Weights.** qllpoc's are tuned for a 62k-work public-library feed with series
   and translators. libcat's default corpus is different. Ship qllpoc's numbers as
   the defaults and expose them as `Options`, or start from subjects-only and add
   signals as they earn their place? *Suggest the latter*: a wrong weight is
   invisible, and "why is this here?" is the only question a librarian will ask.
2. **Should the OPAC precompute final lists or ship the adjacency?** Final lists
   are the static-site-shaped answer and cost `N x limit` ids in a sidecar (~62k x
   8 = 500k ids, a few MB uncompressed, and it gzips well -- see tasks/282).
   Shipping adjacency would need a graph reader in the browser, which libcat does
   not have. *Suggest final lists.*
3. **Does admin need it live at all**, or would the same sidecar do? Live scoring
   over 62k summaries per request is not free; a cached index rebuilt on the
   works-list freshness signal is. But an editor who has just re-subjected a work
   wants to see the neighbours move. *Suggest live, off a cached index.*

## Notes

- Do **not** reach for embeddings. qllpoc needed them because its subjects are
  OverDrive marketing categories; libcat's are LCSH/Homosaurus IRIs with a real
  hierarchy, which is the signal the embeddings were approximating.
- `tools/roargraph/examples/qll_similar.rs:16-89` is a compact native replica of
  the whole algorithm, useful as a reference implementation to test against.
- Benchmark before shipping. `tasks/279` already flags that `project` peaks at
  1.9 GB for a 36-work catalog; adding a corpus-wide step to that build without
  measuring would be irresponsible.
- The DF cap and the `df <= 1` floor are what keep this from being a
  "these two books both have the subject Fiction" machine. They are not tuning
  knobs to skip in a first cut.

## Progress

**The scorer is done** (`similar/`, `e98ae2b`), with `WorkSummary` gaining `Series`
and `Languages`. Eve chose qllpoc's full weight set. Two deliberate divergences
from qllpoc, both pinned by tests and argued in the commit: the singleton floor
counts Works *other than the focus* (qllpoc's `df >= 2` silently drops a
tree-expanded concept held by exactly one other Work), and the DF cap never rounds
away `df = 2` (`floor(0.20 * 5)` is 1, which would leave a five-book catalog with
no rail).

Running it over the real playground caught what 15 green unit tests could not:
`ScanSummaries` over a prefix that also catches `catalog.nq` yields four summaries
per Work, so the focus sat at four offsets, `Neighbors` excluded one, and *Frog and
Toad Together* was the top recommendation for *Frog and Toad Together*. `Build` now
de-duplicates by `WorkID`.

### Benchmarks, before wiring anything (`similar/bench_test.go`)

Synthetic catalog with a realistic attribute spread -- a subject on ~20 Works, a
denser band on ~645, an author on ~8, series on a tenth -- so the DF cap and the
singleton floor both bite.

| n | `Build` | `Neighbors` (one Work) |
|---|---|---|
| 1,000 | 0.46 ms, 1.0 MB | 33 µs, 14 KB |
| 10,000 | 5.6 ms, 14 MB | 202 µs, 94 KB |
| 62,602 | 44 ms, 103 MB | **1.34 ms, 690 KB** |

**The admin half is comfortable.** `Build` once at 44 ms / 103 MB, cached and
rebuilt on the works-list freshness signal, then 1.34 ms per request. That is
cheaper than the grain read the editor is already doing.

**The OPAC half is not, as written.** Neighbours for *every* Work is
`62,602 x 1.34 ms ~= 84 s` serial, churning ~43 GB. Measured directly at n=10,000:
2.18 s and 990 MB allocated for the whole-catalog pass. `lcat project` already
peaks at 1.9 GB for a 36-work catalog (tasks/279); adding 84 s and 43 GB of churn
to that build without saying so would be exactly the "no silent caps" failure.

Per-query cost is dominated by the dense subject band: a heading on 645 of 62,602
Works passes the DF cap (12,520) and drags 645 candidates into the score map on
every hop. Options, cheapest first:

1. **Parallelize the precompute.** Embarrassingly so -- `Neighbors` is read-only on
   a built `Index`. 16 cores takes 84 s to ~6 s. Enough on its own.
2. **Lower `DFCapFraction` for the build.** 0.20 is qllpoc's, tuned for its
   catalog. At 0.02 the dense band drops out entirely. This is a ranking decision,
   not a performance knob, so it should be measured against real neighbours before
   being taken.
3. **Cap the candidate pool.** qllpoc scores a pool of 200 and keeps 8; nothing
   here bounds the score map.

Doing (1) and reporting the number. Not touching (2) without looking at what it
does to the rail.

### Remaining

- `project` emits a `similar.json` sidecar (parallel precompute), bump
  `project.SchemaVersion`, and Hugo renders a section on `page.html` beside the
  asserted `lcat-relations` block -- visually distinct, because these are computed.
- `GET /v1/works/{id}/similar?limit=` (librarian) off a cached index, and a
  `WorkEditor` panel.
- Suppressed Works stay in the admin index and are absent from the projection,
  which happens upstream. Tombstoned Works are already excluded on both.
