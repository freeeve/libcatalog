# 177: Facet counts should update with the active query + filters

Left by the queerbooks-demo session 2026-07-08 (uncommitted cross-repo note).
Eve, browsing v0.32: "when you filter on things the counts aren't updating."

## Done (2026-07-08)

lcat-browse.js now follows the POC's single-pass browse() shape: one ranked
base set (RrsCatalog.search(q, ..., []) for a query, else every doc id),
then RrfFacets.filterIds(base, filters, wantCounts) for the survivors --
the same set drives the result cards AND the live counts.

- Every rendered count (hydrated fragment rows, tree rows, fallback panel)
  repaints from the survivor set; each row's cold number is remembered and
  restored when query + filters clear.
- Active fields recount with their own selections removed (Pagefind-style
  drill-down, as the POC did), so an active field's other values stay
  addable instead of dropping to the intersection's zeros.
- Categories the count wave didn't price (long-tail tree rows) resolve
  exactly via RrfFacets.countsFor on demand.
- Zero-count rows grey out (.lcat-count-zero) rather than hide, so the rail
  stays stable while filtering -- the POC behavior.
- Tree rollups need no special casing: the sidecar's postings are already
  ancestry-expanded (tasks/174), so a parent's live count is the rolled
  subtree count over the survivors.
- Lazy-built rows (tree expansion, panel/tree filter rebuilds, deep-link
  ?q= panel render) repaint on creation.

Verified: 6 new Playwright checks across both e2e profiles (inactive-field
intersection + greying, active-field addability, root rollup recount,
query-only counts, cold restore); 32 checks total green.

---

Original note follows.

lcat-browse.js renders facet counts once from the cold sidecar
(full-corpus numbers) and the apply flow only re-renders results --
selecting a facet or typing a query leaves every count stale, so the rail
promises result sets it will not deliver ("Fiction 11607" inside a
200-work filtered view).

The QLL POC's model (~/qllpoc/site/assets/js/facets.js): one browse() pass
combines query + selected facets and returns the filtered id set; counts
re-derive by intersecting each category's postings with that set (cheap
with roaring bitmaps -- popcount of AND). Tree rollups (174) recompute the
same way over the unioned ancestor postings. Zero-count categories:
POC greyed them rather than hiding, so the rail stays stable while
filtering.

Cold sidebar keeps the full-corpus numbers (nothing to intersect yet);
counts go live once the reader boots and any filter/query is active.
