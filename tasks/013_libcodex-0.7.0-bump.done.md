# 013 -- Bump to libcodex v0.7.0 and confirm the pipeline still works

## Why

libcodex v0.7.0 is released with N-Quads-affecting changes. The libcatalog grain
path rides on libcodex's BIBFRAME emitter (`WorkInstances.Graph`) + `rdf` toolkit,
so any change to emission or canonicalization can move grain bytes. The local
`replace ../libcodex` already tracks the v0.7.0 working tree (HEAD == v0.7.0), so
the build effectively consumes it today; this task makes the version stamp explicit
and re-confirms the invariants hold.

## What changed in libcodex v0.6.0 -> v0.7.0 (relevant to us)

- **Emitter unification** (`bibframe/shape.go` + `shape_render.go` new;
  `graph.go`/`jsonld.go`/`rdfxml.go` rewritten -- libcodex tasks/048+055): Work and
  Instance now serialize through one shared shape traversal. Emission order may
  differ; RDFC-1.0 canonicalization should absorb that (canonical output is
  order-independent), so grain bytes should be unchanged unless the *set* of
  emitted triples changed.
- **Carrier/format on Instance** (`339743a`, libcodex tasks/039): Instances can now
  carry carrier/format. Our OverDrive crosswalk does not set these yet, so no new
  triples expected -- but this **unblocks libcatalog `tasks/011`** (per-Instance
  format facet), which was waiting on it.
- **Multi-instance decode + XML/JSON-LD** (`26c6444`/`9260afa`): not on our grain
  path (we emit N-Quads), no impact expected.
- `rdf/` itself: only new parser/fuzz tests, no production API change.

## Scope

1. Bump `require github.com/freeeve/libcodex v0.6.0 -> v0.7.0` in `go.mod`. Keep the
   local `replace` (roaringrange is still co-developed and unpublished, and the
   published tag was not fetchable offline before); dropping the replaces is a
   separate cleanup once every dep is published-consumable with network.
2. `go mod tidy`, `go build ./...`, `go vet ./...`, `go test ./...`.
3. Reproject the OverDrive corpus and confirm: grains are valid canonical N-Quads,
   the counts are unchanged (5,659 works / 6,253 instances), and re-ingest is still
   byte-stable (the tasks/002 no-churn gate).

## Acceptance

- [x] All packages build and every test passes on v0.7.0.
- [x] Corpus reprojects with unchanged work/instance counts and a byte-stable
  re-ingest.
- [x] Any intended grain-byte change (from the emitter refactor) is identified and
  explained; no unexplained drift.

## Done (commit pending)

`go.mod` require bumped `v0.6.0 -> v0.7.0`; `replace ../libcodex` kept (roaringrange
is still unpublished-consumable, and the local checkout's HEAD already **is** v0.7.0,
so the build tracked it all along). `go.sum` needed no change -- a local-path replace
carries no module hash. Only the one require line moved.

Confirmed on v0.7.0: `go mod tidy`/`build`/`vet` clean; `go test -count=1 ./...` all
pass (fresh, not cached); corpus ingest = **5,659 works / 6,253 instances**
(unchanged); re-ingest minted **0** (no-churn gate holds); and the full downstream is
green -- `serialize` (443,360 quads), `project` (schema v3; 2 languages, 0 controlled
subjects, 6,039 contributors, 0 redirects), `index` (eng 5286 + spa 373). No grain
drift: RDFC-1.0 canonicalization absorbed the emitter-unification reordering, and the
OverDrive crosswalk sets no carrier/format, so libcodex 039 added no triples here.

**Unblocks `tasks/011`** (per-Instance format facet): libcodex v0.7.0 ships
carrier/format on Instance (its task 039), the dependency 011 was waiting on.
