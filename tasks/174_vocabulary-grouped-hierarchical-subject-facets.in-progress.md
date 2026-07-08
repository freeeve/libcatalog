# 174: Subject facets -- vocabulary headers, per-group filter, SKOS hierarchy

Left by the queerbooks-demo session 2026-07-08 (uncommitted cross-repo note).
Eve's design ask for the subject facet UI, with her original QLL POC as the
reference implementation (working code at ~/qllpoc/site/assets/js/facets.js
+ vocab-core.js: fully client-side over roaringrange, facet sidecar for cold
counts, one browse() call combining query AND facets).

Builds on 173 (its item 3 is the baseline scheme grouping); this is the
feature shape beyond the bugfix:

## 1. One facet group per vocabulary, labeled as such

- OPAC sidebar: group controlled subjects by [params.subjectSchemes] with
  the scheme name as the group header ("Homosaurus", "FAST") -- kills the
  duplicate-label confusion (173.3) and matches the POC's
  "HOMOSAURUS SUBJECT" header.
- Admin works view: label the controlled-subjects filter with the
  vocabulary too -- Eve's wording: "Homosaurus (SKOS Vocabulary)" or
  similar, so catalogers see what authority they're filtering by.

## 2. Per-group label filter input

The static fragment already renders `.lcat-facet-filter`
("Filter subjects..." in the POC mock); the hydrated sidebar drops it.
Carry it over -- at ~10k subject values it is the difference between a
usable rail and an endless scroll.

## 3. Hierarchical expansion (SKOS broader/narrower)

POC behavior (screenshot shared with this note): collapsed top-level
concepts with counts; expanding "Gender minorities (1379)" reveals
"Transgender people (1284)" -> "Non-binary people (580)", "Trans women
(188)", ... Each row keeps its own count and (with 173.2) its exclude
toggle.

Plumbing: the graph already carries skos:broader for homosaurus (ingest
emits prefLabel/broader into the feed graph; the projector builds a broader
index) -- but facets.json entries today are {id, labels, scheme, count}
with no broader edge, and the browse-facets sidecar has no hierarchy
either. Both artifacts need the parent links so the hugo sidebar and the
admin rail can render trees without fetching vocabularies client-side.

FAST has no useful hierarchy in our data -- flat list with filter is fine
there; render trees only for schemes whose concepts carry broader links.
