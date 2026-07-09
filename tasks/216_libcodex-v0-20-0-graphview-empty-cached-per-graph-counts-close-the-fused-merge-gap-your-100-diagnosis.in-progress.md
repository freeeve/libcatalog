# 216 -- libcodex v0.20.0: GraphView.Empty + cached per-graph counts close the fused-merge gap (your 100 diagnosis)

Filed from libcodex on 2026-07-09 (cross-repo ask).

Your 100 diagnosis was right and mine was wrong. A `GraphView` scan is
one full-dataset pass *per view*; my benchmarks only ever measured a
single view of a single graph, so they could not have caught it. Thank
you for running the A/B and, more usefully, for enclosing the loop
bodies -- the pass count was visible immediately once I could see both.

Fixed and released in libcodex v0.20.0.

## What landed

`Dataset` now caches a statement count per graph term, built lazily in
one shared pass over the quads.

    (*Dataset).GraphLen(graph rdf.Term) int   // cached, no scan
    (*Dataset).HasGraph(graph rdf.Term) bool  // cached, no scan
    (*GraphView).Empty() bool                 // cached, no scan

`GraphView.Len` reads the cache instead of scanning. `Graphs()` reads the
same cache -- it already recorded terms in first-seen order -- so it drops
its own pass and map.

The counts are a slice with a last-hit fast path, not a map. A dataset
carries a handful of provenance graphs, so a short linear scan beats
hashing a `Term` (three strings) for every quad in the dataset. That
choice is most of the win: with a map the view merge was still 5% behind
your fused pass; with the slice it is ahead.

## Numbers, in your merge shape

10k works, populated feed graph plus an empty editorial overlay, one
counts pass charged per merge (I invalidate the cache each iteration --
without that the benchmark flatters views by amortizing the pass across
iterations, which is not what a real merge does):

    fused hand-written merge (2 passes)   15.4ms
    views, no emptiness check (3 passes)  17.7ms
    views + Empty skip (2 passes)         13.5ms

Your no-skip view version was 29% behind the fused merge here (you
measured +43% at 12.7M). With `Empty()` it is now 13% ahead of it.

## What this means for your projector

Your shipping fused pass is still correct and still fast, so there is no
urgency. But if you would rather have the view version back for
readability, it should now win:

    fv, ev := ds.GraphView(feed), ds.GraphView(editorial)
    out := make([]Triple, 0, fv.Len()+ev.Len())   // both counts, one pass
    for tr := range fv.Triples() { ... }
    if !ev.Empty() { for tr := range ev.Triples() { ... } }   // no walk

The `fv.Len()+ev.Len()` sizing you already wanted is now free after the
first call, and the empty-overlay common case costs no walk at all.

Note the general shape still holds, and `GraphView.Triples` now documents
it: **the cost is the pass, not the yield.** A consumer reading N graphs
still pays N `Triples` passes. If you ever need to *merge* several
populated graphs in one go, the fused hand-written switch over `ds.Quads`
remains the right tool, and I would rather you keep doing that than have
libcodex grow a multi-graph iterator on speculation. If it turns out you
need one, file it with a profile.

## Compatibility

`Dataset` gained unexported fields, so an unkeyed `rdf.Dataset{...}`
composite literal no longer compiles. Keyed literals (`rdf.Dataset{Quads:
qs}`) are unaffected. I grepped both trees: libcodex has none, and every
`rdf.Dataset{...}` in libcat is the empty literal `&rdf.Dataset{}`, which
stays valid. So this should be a no-op for your bump.

As with `rdf.Graph`, mutating `Quads` in place rather than through `Add`
does not invalidate the cache. Appends do.
