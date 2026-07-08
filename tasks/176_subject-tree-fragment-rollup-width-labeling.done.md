# 176: Subject trees (174) -- flat fragment w/ rollup counts, tree width, scheme labeling

Left by the queerbooks-demo session 2026-07-08 (uncommitted cross-repo note).
Eve's screenshot, minutes into the v0.32 sidebar.

## Done (2026-07-08)

Diagnosis against the queerbooks data corrected item 1's premise: facets.json
carries DIRECT counts (Catalog.Facets never rolled up -- homosaurus "Lesbians
8055" vs FAST "Lesbians 6325" is genuine tagging-density difference between
vocabularies, not a rollup). The actual wrong-data source was in the HYDRATED
tree: expandSubjectAncestry mints label-less ancestors into
browse-subjects.json for posting rollups, and on queerbooks 52 of 63
homosaurus tree roots were such nodes -- rendered as raw 40-char authority
URIs with rolled-up counts. Those URI labels are also most of item 2's
overflow.

1. **Minted plumbing nodes never render.** search.BuildBrowse now flags
   minted ancestors (browseSubject.Minted); the tree engine treats a minted,
   still label-less entry as postings plumbing, not a display node -- each
   concept's parent links pass through to the nearest displayable ancestor
   (concept with none becomes a root), ancestry walks and the full-vocab
   filter use the same display graph, and the fallback panel's flat groups
   skip them too. An unlabeled DIRECT subject keeps its id-as-label fallback
   (pre-scheme catalogs use label-like ids). The static fragment stays flat
   at direct counts (the note's option b): its rows link to term pages whose
   membership is direct, so rolled static counts would lie; the tree +
   rollup arrive together at hydration, with structure explaining the
   numbers.
2. **Rail containment.** Nested .lcat-facet-children lists shrink
   (flex-shrink 1, min-width 0, max-width bound by the indent) and rows get
   min-width: 0 -- deep branches can no longer push the group under the
   results column.
3. **Derived parenthetical.** The admin works rail derives the group label
   from the resolved terms it already fetches: "(SKOS Vocabulary)" only when
   a scheme's terms carry broader/narrower edges, "(Controlled Vocabulary)"
   otherwise -- FAST reads as controlled, homosaurus as SKOS.

Follow-up filed as tasks/178: enrichers emit prefLabel/broader for ancestor
chains so minted nodes gain real labels and rejoin the rendered tree.

Verified: e2e fixture grew an unlabeled minted ancestor above the homosaurus
root; 32 Playwright checks across both profiles (incl. "minted ancestor never
renders"), search Go tests cover the Minted flag, svelte-check + 183 vitest.

---

Original note follows.

## 1. Static fragment: rollup counts without the hierarchy that explains them

The shared facets fragment renders flat rows (no nesting, no
data-lcat-broader) but WITH subtree-rollup counts -- "Lesbians 8055" as a
flat list item next to FAST's "Lesbians 6325". Without visible structure
the rolled-up numbers read as wrong data. Either render the tree statically
(collapsed, details/summary nesting -- also the no-JS story) or keep flat
fragments at direct counts and let hydration swap in tree + rollup
together.

## 2. Hydrated tree container overflows the rail

In the screenshot the Homosaurus group (the only scheme with hierarchy)
grows wider than the 16rem track and slides under the results column;
FAST above it stays contained. Indent levels + long labels + count column
need the same shrink/wrap treatment 173 gave the flat rows (max-width:
100%; min-width: 0 on the nested lists).

## 3. Scheme header parenthetical (Eve's question: "is FAST SKOS?")

FAST is distributed as SKOS, so "(SKOS Vocabulary)" is defensible -- but in
the UI it implies hierarchy, which FAST lacks here. Eve's call (confirmed):
derive the label -- "(SKOS Vocabulary)" when the scheme carries broader
edges, "(Controlled Vocabulary)" otherwise. An adopter override string per
scheme ([params.subjectSchemes] / admin scheme config) is welcome on top,
but the derived default is the ask.
