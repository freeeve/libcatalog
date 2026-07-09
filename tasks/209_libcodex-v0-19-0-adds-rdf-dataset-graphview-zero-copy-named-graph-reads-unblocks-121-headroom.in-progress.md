# 209 -- libcodex v0.19.0 adds rdf.Dataset.GraphView, zero-copy named-graph reads (unblocks 121 headroom)

Filed from libcodex on 2026-07-09 (cross-repo ask).

Your tasks/121 profile ask (our 098) is done and released in libcodex
v0.19.0.

## What landed

`(*rdf.Dataset).GraphView(graph rdf.Term) *rdf.GraphView` -- a
read-only view answering the full query surface over int32 positions
into the dataset's own `Quads`, copying no triple. This is the
positions-into-Quads shape you suggested.

    SubjectsOfType(typeIRI string) []Term
    Objects(subject Term, predicate string) []Term
    Object(subject Term, predicate string) (Term, bool)
    Literal(subject Term, predicate string) (string, bool)
    HasType(subject Term, typeIRI string) bool
    Len() int
    Triples() iter.Seq[Triple]
    GraphTerm() Term

`rdf.GraphQuery` is a new interface naming the surface `*rdf.Graph` and
`*rdf.GraphView` share (the five query methods), with compile-time
assertions on both. Write `func scan(g rdf.GraphQuery)` once and pass
either.

`rdf.Dataset.Graph` is unchanged -- still correct where you need to own
or mutate the triples.

## Numbers

Corpus bench, 10k works / ~325k quads, split one named graph and query
it (Apple M3 Max):

    scan (SubjectsOfType)    26.4ms  253MB  ->   3.3ms   5.0MB
    subject lookup (Object)  46.6ms  264MB  ->  26.8ms  11.5MB

Both paths are benchmarked on purpose. Measuring only the scan would
have flattered the change: ScanGrain leans on Object/Literal, and those
build an index on both the old and new path, so the win there is 1.7x
time / 23x memory rather than 8x / 50x.

## One implementation note that matters for your expectations

The obvious version -- eager subject index on first touch, like
`Graph.index()` -- came out **slower than `Dataset.Graph`** (30.8ms vs
23.9ms) while allocating 14x less. `Graph.SubjectsOfType` never builds
the subject index; it scans. A view that forced a `map[Term]` build on
every query was paying for an index the hot call does not use.

So the laziness is split: subject-keyed lookups
(Object/Objects/Literal/HasType) build and cache the index; whole-graph
scans (Len/Triples/SubjectsOfType) never trigger it and allocate
nothing beyond their result. If you hold a view per grain and only call
SubjectsOfType, you now pay no index at all.

## Notes for adoption

- `splitGraphs` can go away without parse-time segmentation:
  `GraphView.Triples()` is an `iter.Seq[Triple]` that yields straight
  from the dataset's quads, and `d.Graphs()` still enumerates the graph
  terms. Range one view per graph term.
- ScanGrain's per-graph QUERY semantics (feed vs editorial separation)
  are preserved exactly: a view never reports another graph's
  statements, including for a subject and predicate the two graphs
  share. That is pinned by a test.
- Appending to the dataset invalidates a view's cached index and the
  next query rebuilds it. In-place quad mutation does not -- the same
  contract `rdf.Graph` already has. Views are not safe against a
  concurrent writer, and the first subject-keyed query builds the
  index, so concurrent readers should either share a view whose index
  is already warm or hold one view each.
- A view borrows the dataset; the dataset must outlive it. Combined
  with `ParseNQuadsShared`, the input buffer must outlive both.

## Deliberately not done: parse-time segmentation

You floated bucketing quads per graph at parse so views become plain
slices. Skipped, and I would push back on it: `Dataset.NQuads` writes
quads in slice order, so bucketing would silently reorder the output of
`ParseNQuads(x).NQuads()` for any document whose graphs interleave --
an observable change to serialization, for a second-order allocation
win on top of the numbers above. Your own ask notes the query-surface
view alone removes the top per-grain allocator, and it does.

If you still want segmentation for the 10M-work tier, file it as a
separate opt-in constructor (e.g. `ParseNQuadsSegmented`) rather than a
change to how `ParseNQuads` orders a dataset, and bring a profile taken
from the view-based code so we are optimizing what is actually left.
