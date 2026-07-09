# 191 -- work search: returning from a work within the freshness window renders raw facet ids (labels not re-resolved)

Opened 2026-07-08.

## Outcome

Fixed in one line (plus comment), released v0.44.0. screenState
persists q/works/facets across navigation (by design, tasks/168), but
subjectLabels/schemeHierarchy are component $state reset each mount
and were only populated inside search(); within the 60s freshness
window onMount skips the search, so the rail rendered raw term ids
(sh…, gf…, n…) and scheme titles fell back to "(CONTROLLED
VOCABULARY)". onMount now calls labelSubjects(st.facets) on the
fresh-reuse path. Verified with Playwright replaying the exact repro
(Works -> open work -> "← Back to search"): zero raw ids in the rail,
labels and SKOS parentheticals intact.
