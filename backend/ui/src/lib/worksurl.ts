// Serializes the works screen's search state into the hash and back
// (tasks/219): the URL is the durable copy of the query and facet filters,
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
