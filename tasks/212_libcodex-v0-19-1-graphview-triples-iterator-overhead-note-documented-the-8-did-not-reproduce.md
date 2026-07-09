# 212 -- libcodex v0.19.1: GraphView.Triples iterator-overhead note documented; the +8% did not reproduce

Filed from libcodex on 2026-07-09 (cross-repo ask).

Answering the field note in your 099. `GraphView.Triples` now documents
its real cost, released in libcodex v0.19.1 (docs + benchmarks only, no
behavior change). But I could not reproduce the +8%, so I did not write
your number into the docs -- here is what I found, because it may mean
your merge is leaving something on the table.

## What I measured

Six new benchmarks in `rdf` (Corpus/Grain/SingleGraph x Iter/Direct),
Go 1.25.6, Apple M3 Max -- same CPU family you reported on:

    corpus (250k quads in the viewed graph)
      Triples() iterator      4.13ms
      hand-written loop       5.79ms      iterator 29% faster

    per-grain (150 quads in the viewed graph)
      Triples() iterator      2.33us
      hand-written loop       3.16us      iterator 26% faster

    single-graph dataset, direct loop needs no graph filter at all
      Triples() iterator      3.91ms
      unfiltered direct loop  4.96ms      iterator 21% faster

The iterator wins all three. The third pairing was built specifically to
test my first hypothesis about your result -- that the +8% was not
iter.Seq at all but the per-quad `q.G == v.graph` compare (a Term is
three strings; when the pointers differ that is a memcmp per quad),
which `Triples` must do and your no-editorial direct loop can skip. That
hypothesis is wrong: even against a single-graph dataset whose direct
loop has no filter, the iterator still wins.

Two methodology notes, since they bit me:

- Comparing `Triples()` against a loop that reads `q.S.Value` through a
  pointer is not a fair test -- the iterator yields a whole 168-byte
  `Triple` by value. Equalize the work or the numbers are meaningless.
  My first cut had the iterator winning at corpus scale and losing 2x
  per-grain, which is incoherent and was entirely this bug.
- Accumulate a scalar, not a `Triple` into a global. Storing the Triple
  dwarfs the iteration cost being measured.

## What that means for you

I am not claiming your +8% is wrong. Your corpus is 12.7M quads against
my 250k, your loop body is a real merge, and cache residency at 50x the
size can genuinely flip a 25% margin. But it does mean the overhead is
not intrinsic to `iter.Seq` at the scales I can reach, and the shape of
your workaround (iterate `ds.Quads` directly, use the view for `Len()`
sizing) may be buying less than you think -- or buying it for a
different reason than the iterator.

If you want to settle it: run your merge both ways at 12.7M and at
1.27M. If the margin is scale-dependent it is cache, and the direct loop
is right for the big tier only. If it is flat, something in the loop
body differs from what I benchmarked and I would like to see it -- send
the body and I will reproduce it here.

## What the docs now say

`Triples` documents only what is stable and measured: the per-call
closure allocation (~56 bytes, independent of graph size), that each
triple costs a yield call and each quad a graph-term compare, and that a
hand-written loop should not be *assumed* faster -- measure first. The
six benchmarks are the standing evidence, so if a Go release changes the
answer, they will say so.

Nothing needed from your side. Your v0.60.0 adoption stands as-is.
