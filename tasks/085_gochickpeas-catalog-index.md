# 085: Catalog-scale graph index (gochickpeas) behind the projection seam

## Context

Grain-scoped reads need no store: every path parses the grain's N-Quads
(libcodex rdf.ParseNQuads) into a flat []Quad and linear-scans (editor
claims, override scans, marcview group diffs). A grain is a few hundred
quads, so this is microseconds and dependency-free -- keep it.

What does not scale is the catalog level:
- Every publish reruns LCATD_REBUILD_CMD, which reserializes and reprojects
  the whole site (lcat serialize && lcat project over every grain). Fine at
  30 grains; a bottleneck at real holdings scale.
- Cross-work queries have no home: "works sharing this agent/subject",
  authority neighborhoods across the corpus, duplicate sweeps would each
  mean scanning every grain file.

gochickpeas (sibling repo) offers an nq reader/writer and fast direct-Go
traversals/queries -- a candidate first backend.

## Design sketch

- Blob grains stay the source of truth. The clean-diff invariant lives in
  RDFC-1.0 canonical bytes; the graph store is a derived, rebuildable
  index, never written by the editor path.
- Introduce a small interface at the projection/reingest seam (e.g.
  catalogindex.Store): UpsertGrain(workID, quads) / DeleteGrain(workID) /
  the query shapes projection and search facets need. lcat project, facet
  building, and future cross-work endpoints consume the interface; grain
  editing never touches it.
- Requirements on the backend:
  - named graphs first-class -- provenance is quads (feed:*, editorial:,
    enrichment:*), not triples;
  - atomic per-grain replace, so re-ingest swaps a grain's statements
    without a full rebuild;
  - deterministic iteration for stable projection output.
- First consumers, in order:
  1. incremental reprojection on publish (replace the whole-site rebuild
     loop with upsert + project-changed);
  2. "works sharing agent/subject" endpoints (works browse, authority
     usage counts);
  3. duplicate sweeps over identifier/title keys.
- Open questions: embedded vs sidecar; persist the index or rebuild on
  boot (rebuild is always correct -- start there); memory bounds at 100k+
  works; whether gochickpeas exposes quad (not triple) storage natively.

Per the multi-repo rule: any gochickpeas-side change gets an uncommitted
task file in that repo, not edits from this session.

## Sizing evidence (measured 2026-07-04)

Ran the real pipeline (lcat ingest overdrive -> serialize -> project) over
two full real library catalogs, sourced from deeplibby's pebble store
(`~/deeplibby/api/dl.pebble`): each library's `library_media` set exported
to OverDrive-Thunder-shaped page JSON, then ingested unchanged. Apple
M-series, single process, in-memory datastore + DirStore grains.

| stage | NYPL | LAPL | ratio | per-work |
|---|---|---|---|---|
| works (from instances) | 235,488 (of 265,086) | 487,901 (of 569,896) | 2.07x | -- |
| quads (catalog.nq) | 15.40M | 31.86M | 2.07x | **65.3 q/work** |
| catalog.nq on disk | 1.7 GB | 3.4 GB | 2.0x | ~107 B/quad |
| ingest wall | 118 s | 223 s | 1.9x | ~0.46 ms/work |
| **ingest peak RSS** | 8.6 GB | 18.0 GB | 2.09x | **~37 KB/work** |
| serialize wall | 21 s | 85 s | 4.1x | IO-bound (see note) |
| serialize peak RSS | 0.16 GB | 0.28 GB | -- | streams; sub-GB |
| project wall | 21 s | 45 s | 2.2x | ~90 us/work |
| **project peak RSS** | 13.7 GB | 28.0 GB | 2.05x | **~57 KB/work** |
| catalog.json | 222 MB | 460 MB | 2.07x | ~0.94 KB/work |

Reads off two clean points:
- **Quads/work is constant (~65)** on the OverDrive/Thunder route, so
  everything downstream scales linearly with work count. (MARC-Express
  grains are richer -- ~125 quads/work measured on the QLL sample -- so a
  MARC-sourced full collection roughly doubles every quad-derived number.)
- **Memory is the wall, not CPU.** Both hot stages load the whole catalog:
  `project` (rdf.ParseNQuadsShared over the entire catalog.nq + catalog.json
  built in RAM) grows at ~57 KB/work; `ingest` (identity resolver + grains
  in flight) at ~37 KB/work. Both linear across the two points.
- `serialize` streams (sub-GB RSS) but its wall time is super-linear here
  (4.1x for 2.07x works) -- IO-bound on the grain-tree walk (487k vs 235k
  small files), cache-sensitive; needs its own measurement if it matters.
  It is not the scaling wall.

Extrapolation to a ~10M-work full-OverDrive corpus (~20x LAPL, Thunder
route; linear stages):

| | ~10M works |
|---|---|
| quads / catalog.nq | ~650M / ~70 GB |
| catalog.json | ~9 GB |
| ingest | ~75 min CPU / **~370 GB RAM** |
| serialize | ~15-30 min / low RAM |
| **project** | ~15 min CPU / **~570 GB RAM** |

So the whole-site reproject is CPU-tractable (a full rebuild is ~1-2 h; the
per-publish `serialize && project` is ~45 min at 10M) but **memory-infeasible
past ~1-2M works on one box** -- LAPL alone (488k works) already needs 28 GB
just to project, and LAPL+NYPL+Brooklyn (~1.3M works) would want ~75 GB. And
this cost is paid *per publish*, since LCATD_REBUILD_CMD reprojects the whole
corpus on every edit.

This answers the "memory bounds at 100k+ works" open question above and is
the quantified case for the incremental `catalogindex.Store` seam: upsert the
one changed grain and project only affected works, replacing a
~45-min/570-GB whole-corpus operation with a per-grain one. Repro harness
(exporter + run scripts) is in the session scratchpad, not committed.
