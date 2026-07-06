# 143 -- deployment-defined facet dimensions (extras -> facets)

Filed from queerbooks-demo (2026-07-06, Eve's ask). Do not let a queerbooks
session edit this repo -- implement here.

## Ask

queerbooks wants a **Sources** facet (Mombian, Queer Books Database, Queer
Lit, ... -- which community database(s) attested each work). The data rides
extras today (`sources: "loc,mombian,..."` on every work), but extras are
display-only: a deployment cannot turn one into a facet without a new
taxonomy, and the adapter mints taxonomies only from the fixed catalog.json
fields.

## Suggested shape

Site-config-driven extra facet dimensions, e.g.

    [params.extraFacets.sources]
      extra = "sources"      # extras key
      split = ","            # multi-valued
      title = "Sources"      # facet group heading (i18n-able)

The adapter mints a taxonomy per entry (values slugged via lcat-slug, term
pages like any other dimension) and facets.html renders the group. Counts
come free from Hugo's taxonomy machinery. [taxonomies] merge gotcha
(tasks/133) applies -- these are site-side dimensions, so that is naturally
the site's config anyway.

queerbooks interim: none -- source tags in the genres&tags facet would put
1-4 source chips on all 48.5k works and drown the real tags; waiting on the
feature is better. (Their per-edition QLL chips shipped via tags because
those ARE genre-ish discovery terms; sources are a distinct dimension.)

## Done

Implemented as suggested, site-config-driven, no projector/schema change:

- Adapter (_content.gotmpl): for each [params.extraFacets.<name>], split the
  extras value (cfg.extra, default <name>; cfg.split optional), trim, dedupe,
  and set page params <name> = lcat-slug keys (URL-safe taxonomy terms,
  tasks/023 precedent) + <name>Labels = raw text, index-aligned (the
  tags/tagLabels pattern). Precedence: reserved params > extra-facet params >
  raw extras (the slice replaces the raw string under the same key).
- facets.html renders one group per entry from site.Taxonomies (counts free
  from Hugo, .ByCount order matches facetValues). Heading: i18n key <name> >
  cfg.title > humanize. term.html/taxonomy.html resolve term display labels
  via new partial lcat-extra-facet-label.html -- zips the slug back to raw
  text through the first member work's parallel params, O(1) per term.
- The site declares the matching [taxonomies] entry itself (the tasks/133
  merge gotcha means it must anyway); a missing entry = no group, not a
  broken build.
- exampleSite demonstrates: source = "sources" taxonomy, extraFacets block,
  fixture works carry extra.sources CSVs, es.toml translates the heading
  ("Fuentes"). Verified: sidebar group + counts (Mombian 2, Queer Lit 2,
  QBD 1), /sources/<slug>/ term pages titled with raw labels, landing page
  labeled; link check + a11y audit pass (117 pages). Documented in
  hugo/README.md and hugo/hugo.toml.
