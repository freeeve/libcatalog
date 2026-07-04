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
