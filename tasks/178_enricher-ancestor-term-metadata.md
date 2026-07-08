# 178: Enrichers emit label/broader metadata for ancestor terms

Follow-up to tasks/176. expandSubjectAncestry (search.BuildBrowse) mints
skos:broader ancestors that no feed ever described: enrichmentQuads
(ingest/enrich.go) emits prefLabel + broader only for a Work's DIRECT
subject terms, so an ancestor that is never itself a direct subject reaches
browse-subjects.json label-less. On queerbooks that was 52 of 63 homosaurus
tree roots; 176 keeps such nodes out of the rendered tree (pass-through
parents), which means real vocabulary structure is invisible until some
work happens to carry the ancestor directly.

The fix at the source: when an enricher resolves a subject, walk its
broader chain in the vocabulary (vocabsrc has the full dump in the vocab
index; the suggest-API enricher path may need a lookup per ancestor) and
emit each ancestor's prefLabel + broader quads into the enrichment graph
too -- depth-capped like ancestryDepthCap. The terms are pure authority
metadata, so co-grained preservation semantics are unchanged; grains grow
by a few quads per chain.

Plumbing note: emitting the quads is not enough on its own. Work.Subjects
carries only direct terms, so BuildBrowse mints ancestors from per-work
metadata and never sees the ancestor labels even when the graph has them.
The projection needs a vocabulary sideband (e.g. Catalog.Terms: every term
URI seen in the graph with labels + broader, which the projector's
labels/broader indexes already contain) that BuildBrowse consults when
minting -- likely a schema bump.

Then: minted-but-labeled entries render as normal tree nodes (176's
display rule already handles this -- Minted entries WITH labels display),
and the homosaurus tree gets its real top levels back.

Consumers with their own pipelines (queerbooks-demo ingests directly) need
the equivalent change on their side; noted in their tasks/026.
