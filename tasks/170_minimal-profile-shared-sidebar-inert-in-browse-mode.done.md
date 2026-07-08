# 170: Shared facets sidebar is inert under minimal profile + roaringrange

Left by the queerbooks-demo session 2026-07-08 (uncommitted cross-repo note).

## Shipped (2026-07-08) -- the hydration option

- facets-body.html emits `data-lcat-field`/`data-lcat-cat` (the reader's
  [field, category] filter pair; subjects key by id, the rest by raw value)
  on exactly the rows it could not link -- page-invariant, and zero new
  bytes where term pages exist.
- lcat-browse.js, after the reader boots, hydrates those rows into checkbox
  toggles (value + count move into a label, the hidden negatives button
  stays put) and skips rendering the duplicate `#lcat-browse-facets` panel;
  the panel remains the fallback when no unlinked rows exist. `selected()`
  reads panel and sidebar toggles alike.
- Shared-mode timing: lcat-sidebar.js now dispatches `lcat:facets-loaded`
  after inserting the fragment and re-activating its scripts;
  lcat-browse.js hydrates on that signal too, tearing down the panel if it
  had already rendered (whichever of fragment-fetch and reader-boot
  finishes last completes the takeover).
- Extra-facet groups (params.extraFacets) have no reader field; with no
  term pages they are skipped entirely instead of rendering a dead group.
- No-JS / reader-down: rows stay plain text -- same as before, minus the
  panel; nothing regresses.

Tests: two new jsdom cases (event fires once after script re-activation,
never on fetch failure); new e2e pass in run.sh building the exampleSite
under the minimal profile + shared sidebar, driving hydration, panel
suppression, toggle filtering, query intersection, and static restore in
Chromium (5 checks; the existing non-minimal pass still covers the panel
path). README minimal-profile section updated. queerbooks-demo task 021
filed (uncommitted, their repo) to drop the empty facets.html shadow after
adopting the release.

## Symptom

With `disableKinds = ["taxonomy","term"]` (157 minimal profile) and
`[params.search] engine = "roaringrange"` (158) plus the shared sidebar
(150), list pages render TWO facet UIs:

- the shared static sidebar fragment: facet values emit as
  `<span class="lcat-facet-value">` (correct -- no term pages to link to),
  with `data-lcat-term` attrs and hidden exclude buttons that no shipped JS
  ever hydrates in browse mode. It LOOKS like the primary facet rail and is
  completely dead -- first thing a user clicks ("the facets aren't
  clickable", Eve, 5 minutes into browsing).
- `#lcat-browse-facets`: the functional checkbox panel lcat-browse.js
  renders above the results after the reader boots.

## Ask

In browse mode the static sidebar should either not render (list templates
skip the facets partial when engine=roaringrange + taxonomy kinds disabled)
or hydrate: wire the fragment's values as toggles driving the reader,
replacing the duplicate panel -- the sidebar is the better home for facets
anyway. Either resolves the dead-UI trap; we'd prefer the hydration.

Workaround deployed in queerbooks-demo: the site shadows
`_partials/facets.html` empty, leaving the browse panel as the only facet
UI. Drop the shadow once this lands.
