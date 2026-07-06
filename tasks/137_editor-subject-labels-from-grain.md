# 137 -- editor: FAST (any authority) subject names from the grain, not only
# the installed vocab index

Filed from queerbooks-demo (2026-07-06, Eve's report while cataloging). Do
not let a queerbooks session edit this repo -- implement here.

**Progress:** fix 1 (grain fallback) landed with tasks/140 (v0.11.0): the
doc mapper resolves a subject IRI's grain-written skos:prefLabel as a
display annotation and the editor chips fall back to it when the vocab
index misses. Remaining here: fix 2, `lcat vocab-subset --from-catalog`.

## Symptom

Record editor on the coll corpus: FAST subjects render as bare
`id.worldcat.org > fast/1985432` chips flagged "not in local index", while
homosaurus chips show names -- only because that deployment installed the
homosaurus snapshot. But the FAST names ARE in the corpus: the ingest
emission writes `<authority-iri> skos:prefLabel "..."@en` into the feed
graph next to every bf:subject link (graphcorpus.go), and the projector/
static site display them fine. The editor's label resolution consults ONLY
vocab.Index (installed snapshots), so grain-carried labels are invisible.

## Suggested fixes (complementary)

1. **Fall back to the grain.** When the terms/resolve path misses the vocab
   index, read the work grain's own skos:prefLabel for that URI (the editor
   already has the grain in hand). Cheap, no install required, fixes every
   authority the corpus carries labels for. Keep the "not in local index"
   hint (it still signals no browse/hierarchy/typeahead), but show the name.
2. **`lcat vocab-subset --from-catalog`.** Both existing modes hit the
   network (per-term fetch; whole-vocab dump), but catalog.json already
   carries `subjects[].labels` for every used term. A mode that emits the
   snapshot purely from catalog.json makes a corpus-sized FAST index in
   milliseconds with zero OCLC dependency (their per-term linked-data
   endpoints are flaky/retired anyway; searchFAST typeahead per tasks/132
   covers assignment of NEW terms).

## Workaround deployed queerbooks-side meanwhile

Generated `<uri> skos:prefLabel "..."@en` N-Quads from catalog.json's fast
subjects and PUT them as a `fast` snapshot via /v1/vocabsources -- works,
but it is exactly what --from-catalog should be.
