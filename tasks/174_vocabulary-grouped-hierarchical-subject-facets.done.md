# 174: Subject facets -- vocabulary headers, per-group filter, SKOS hierarchy

## Done (2026-07-08, v0.32.0)

Ported the POC's counting model rather than its label-keyed trails: BuildBrowse
now unions each subject's postings into its skos:broader ancestors (parents
carry rolled-up counts; a parent filter, include OR exclude, covers the
subtree -- exactly index.searchsource.json's ancestry expansion, done
build-side), and browse-subjects.json carries labels + scheme + broader per
concept. facets.json already had SubjectFacet.Broader since schema v5 -- the
"no broader edge" premise below was stale; no schema bump was needed.

1. **Vocabulary headers**: OPAC groups were done in 141/173; the admin works
   view now resolves each subject IRI to its scheme (vocab index plumbed into
   the facet counter, top-N capped per scheme) and renders one rail group per
   vocabulary, legend "<Scheme> (SKOS vocabulary)".
2. **Per-group filter**: tree schemes filter the FULL vocabulary client-side
   (matches render under forced-open ancestors, POC computeHomoVisible);
   flat schemes and the fallback panel filter their full category list; the
   admin rail gained a type-to-filter on any group past ten values.
3. **Hierarchy**: hydrated sidebar and fallback panel render schemes with
   broader links as lazy-expanding trees (top ROOTS_SHOWN=20 roots by rolled
   count, caret to expand, per-row exclude with 173's negatives, selections
   survive filter rebuilds). FAST stays flat, as specified.

Verified: 24 Playwright checks across both e2e passes (tree counts,
subtree exclusion, full-vocab filter, selection continuity), search/httpapi
Go tests, svelte-check + 183 vitest tests, jsdom sidebar tests.

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
