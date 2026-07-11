// Serializes the works screen's search state into the hash and back
//: the URL is the durable copy of the query and facet filters,
// so a reload, a bookmark, or a pasted link restores the same result set.
// Filter groups ride as repeated params (holdings=none&sources=a&sources=b),
// the same shape /v1/works takes; screenState stays the fast path for
// in-app Back.
import type { WorkFilters } from "./api";

/** The canonical works hash for a search state; "#/works" when empty. */
export function worksHash(q: string, filters: WorkFilters): string {
  const sp = new URLSearchParams();
  const trimmed = q.trim();
  if (trimmed) sp.set("q", trimmed);
  for (const group of Object.keys(filters).sort()) {
    for (const v of filters[group]) sp.append(group, v);
  }
  const qs = sp.toString();
  return "#/works" + (qs ? "?" + qs : "");
}

/** Facet params ride to #/exports under this prefix so they cannot collide with
 *  the exports screen's own kind/q/ids/sq params. */
const FACET_PREFIX = "f.";

/** The hash for "Export these results…".
 *
 *  It carries the facet filters and the tombstoned mode, not just the query. The
 *  link is labelled "these results", and before this it pointed at kind=all --
 *  a cataloger who had narrowed 62,602 works to 465 was handed the 62,602.
 *
 *  An empty query becomes kind=all rather than kind=search, because a search
 * selection with no query is refused by the server on purpose.
 *  "Everything, filtered" is kind=all plus facets. */
export function exportsHash(q: string, filters: WorkFilters, showTombstoned: boolean): string {
  const sp = new URLSearchParams();
  const trimmed = q.trim();
  sp.set("kind", trimmed ? "search" : "all");
  if (trimmed) sp.set("q", trimmed);
  for (const group of Object.keys(filters).sort()) {
    for (const v of filters[group]) sp.append(FACET_PREFIX + group, v);
  }
  // The works screen hides retired records unless asked, so the
  // export must too, or its preview count will not match the count beside it.
  if (!showTombstoned) sp.set("tombstoned", "exclude");
  return "#/exports?" + sp.toString();
}

/** Reads the facet filters back out of an #/exports hash. */
export function parseExportFacets(query: URLSearchParams): WorkFilters {
  const filters: WorkFilters = {};
  for (const key of new Set(query.keys())) {
    if (!key.startsWith(FACET_PREFIX)) continue;
    const group = key.slice(FACET_PREFIX.length);
    if (group) filters[group] = query.getAll(key);
  }
  return filters;
}

/** Parses a works hash query back into search state; null when the URL
 *  carries no state (a plain #/works keeps whatever the screen remembers). */
export function parseWorksQuery(query: URLSearchParams): { q: string; filters: WorkFilters } | null {
  const keys = [...new Set(query.keys())];
  if (keys.length === 0) return null;
  const filters: WorkFilters = {};
  for (const key of keys) {
    if (key === "q") continue;
    filters[key] = query.getAll(key);
  }
  return { q: query.get("q") ?? "", filters };
}
