# 286 -- libcodex v0.24.0: rdf.Graph.Objects now returns distinct objects, which fixes the s.Items over-count in ingest/enrich.go on inputs with repeated statements

Filed from libcodex on 2026-07-10 (cross-repo ask).

Behavior change in `rdf.Graph`, worth a read before bumping. Nothing here breaks
on the bump; one thing quietly gets *less* wrong.

## The change

`rdf.Graph` never was the set its doc claimed. RDF 1.1 defines a graph as "a set
of RDF triples", but `Graph.Triples` is a slice every parser appends to, and real
serializations restate triples constantly. LC's own marc2bibframe2 output
re-describes a shared node under every property that references it -- one of their
N-Triples fixtures is 449 lines for 389 distinct triples, with a single triple
appearing sixteen times.

Deduplicating on parse was measured and rejected (2.5-4x slower, +63% memory, and
it turns the corpus parse from 5 allocations into 331k). So `Graph` keeps the
document's list and now says so. What changed is the query surface:

- `Graph.Objects` and `GraphView.Objects` return **each distinct object once**.
- `Graph.ObjectsWithRepeats` is the new statement-for-statement list view.
- `Graph.Dedupe()` collapses the triples to a set on request and reports how many
  it removed. `Graph.Canonical()` already did this internally.
- `Graph.SubjectsOfType` already deduplicated; unchanged.

## Why you specifically

`ingest/enrich.go:430`:

```go
s.Items += len(merged.Objects(inst, bfNS+"hasItem"))
```

That is a count taken straight off the returned slice. Before v0.24.0, an input
that stated `<inst> bf:hasItem <x>` twice made `s.Items` count `x` twice. After,
it counts once. The same reasoning covers the other `Objects()` call sites --
`identity/scan.go:110,161,176` and `ingest/enrich.go:392,399,411,416,417`.

Note `ingest/enrich.go:361` builds `merged.Triples` by appending quads from
several graphs:

```go
merged.Triples = append(merged.Triples, rdf.Triple{S: q.S, P: q.P, O: q.O})
```

If two source graphs carry the same statement, `merged` holds it twice. That was
already true; it just no longer leaks into `Objects` results. If you want
`len(merged.Triples)` to mean "distinct statements", call `merged.Dedupe()` after
the merge loop -- one pass, and it tells you how many it removed.

## What to check when you bump

Anywhere you relied on `Objects` returning one term per statement. In libcodex
exactly one place did: bibframe's positional `bf:seriesEnumeration`, aligned index
for index with `bf:seriesStatement`, so two 490s carrying the same `$v` encode to
two identical triples. It now calls `ObjectsWithRepeats`.

That case is worth knowing about, because it is a modeling flaw rather than an API
detail. Multiplicity is not something RDF's abstract syntax carries, so that field
round-trips through libcodex and is silently lossy through rdflib, Jena, or any
other set-backed store -- our own tests were the one configuration that could not
see it. Tracked as libcodex 110, with LC's real model (one `bf:Relation` node per
490, enumeration attached to the relation rather than the Instance) as the fix. If
the projector reads `bf:seriesStatement` off an Instance, that shape will change
when 110 lands, and that release will say so.

## Bump

`go get github.com/freeeve/libcodex@v0.24.0`, both modules together.
