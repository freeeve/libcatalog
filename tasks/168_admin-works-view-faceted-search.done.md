# 168: Faceted search in the backend admin works view

The public site has faceted browse; the cataloging UI's works view has only
the flat search. Facets there would pull double duty as *working* filters:
"everything missing a subject", "all works from feed:overdrive touched by
editorial:", "format = audiobook with no ISBN" -- the shapes catalogers
slice by when triaging.

Sketch:

- Facet source: the workindex already holds per-work summaries resident and
  serves the works list -- derive counts from what it carries (scheme,
  format, language, feed/provenance, has-/missing-field predicates) rather
  than introducing a second index. If a wanted facet is not in
  ingest.WorkSummary, extending the summary at scan time is the move (it
  reprojects via the snapshot/feed path, tasks/156).
- Cataloger-shaped facets first: provenance graph (feed:*/editorial:/
  enrichment:*), completeness (missing subject / classification / ISBN /
  cover), format, language, scheme -- not a copy of the public site's
  patron facets.
- UI: filter rail on the works view (backend/ui); counts update with the
  query; multiple selections AND across facets, OR within one.
- API: extend the works-list endpoint with facet counts + filter params so
  the SPA stays one round trip per interaction.
- Keep an eye on tasks/167: if the vocab/catalog indexes move to
  roaringrange sidecars, facet bitmaps (RRSF) could serve this too --
  don't build anything that blocks that convergence.
