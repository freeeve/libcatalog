# 009 -- Hugo module: content adapter, layouts, facet UI

## Context

`lcat project` now emits `catalog.json` (one record per Work: title, contributors,
subjects, languages, BISAC classifications, instances with ISBNs + provider ids).
That is the contract this task consumes (ARCHITECTURE §7). Parallelizable with
`tasks/010` (search) and `tasks/004` (availability) once the JSON shape is stable.

## Scope

The `hugo/` module (`hugo mod get github.com/freeeve/libcatalog/hugo`), its own
`go.mod` so Hugo sites don't pull the Go build deps:

1. **Content adapter** (`_content.gotmpl`, Hugo >= 0.126): mint a Page per Work
   from `catalog.json` -- no content files, no per-record markdown.
2. **Layouts**: a faceted, paginated Work list and a Work detail page (format
   facets from the Instances, live-availability placeholder, subjects/contributors
   as links).
3. **Partials/assets**: facet sidebar (language / format / subject / contributor),
   search box wired to the roaringrange WASM reader (`tasks/010`), and the
   availability JS hook (`tasks/004`).
4. **Accessible by default** (§2): semantic HTML, ARIA on facet/search UI, full
   keyboard nav, adequate contrast -- a build-time constraint.

## Facets

Facet values/counts derive from `catalog.json`; decide client-side aggregation vs
a projector-emitted `facets.json` (precomputed counts scale better for large
corpora -- a small `project/` follow-up if needed).

## Acceptance

- [ ] `hugo mod get` + content adapter renders one page per Work from catalog.json.
- [ ] Facets filter the list; Work detail shows its Instances/formats.
- [ ] No per-record content files; theme overrides layer cleanly on top.
- [ ] Axe/Lighthouse a11y pass on list + detail.
