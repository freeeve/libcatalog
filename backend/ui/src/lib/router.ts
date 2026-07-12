// A tiny hash router: no history-API rewrites needed beyond the go:embed
// fallback, deep links work from a static file server. Patterns are literal
// segments plus ":param" captures; the first match wins.

export interface RouteMatch {
  name: string;
  params: Record<string, string>;
  query: URLSearchParams;
}

export interface RouteDef {
  name: string;
  pattern: string; // e.g. "/works/:id"
}

/** Every route the SPA resolves, in match order. Lives here rather than in
 *  App.svelte so the navigable ones can be pinned against lib/screens.ts: three
 * hand-maintained navigation lists had drifted apart. */
export const ROUTES: RouteDef[] = [
  { name: "dashboard", pattern: "/" },
  { name: "login", pattern: "/login" },
  { name: "callback", pattern: "/callback" },
  { name: "works", pattern: "/works" },
  { name: "work", pattern: "/works/:id" },
  { name: "authorities", pattern: "/authorities" },
  { name: "authority", pattern: "/authorities/:id" },
  { name: "vocabsources", pattern: "/vocabularies" },
  { name: "batch", pattern: "/batch" },
  { name: "macros", pattern: "/macros" },
  { name: "exports", pattern: "/exports" },
  { name: "copycat", pattern: "/copycat" },
  { name: "newrecord", pattern: "/copycat/new" },
  { name: "duplicates", pattern: "/duplicates" },
  { name: "withdrawals", pattern: "/withdrawals" },
  { name: "queue", pattern: "/queue" },
  { name: "promotions", pattern: "/promotions" },
  { name: "profiles", pattern: "/profiles" },
  { name: "suggestions", pattern: "/suggestions" },
  { name: "audit", pattern: "/audit" },
  { name: "diversity", pattern: "/diversity" },
  { name: "diversityconfig", pattern: "/diversity/config" },
  { name: "enrichment", pattern: "/enrichment" },
];

/** Routes nothing navigates to by name: auth callbacks and detail pages a user
 *  reaches by clicking a record, never by asking for "that screen". Everything
 *  else must appear in SCREENS. */
export const NON_SCREEN_ROUTES = new Set(["login", "callback", "work", "authority", "newrecord"]);

/** Splits "#/works/w1?tab=2" into path and query. */
export function parseHash(hash: string): { path: string; query: URLSearchParams } {
  let h = hash.startsWith("#") ? hash.slice(1) : hash;
  if (h === "") h = "/";
  const qi = h.indexOf("?");
  const path = qi >= 0 ? h.slice(0, qi) : h;
  const query = new URLSearchParams(qi >= 0 ? h.slice(qi + 1) : "");
  return { path: path.startsWith("/") ? path : "/" + path, query };
}

/** Matches one pattern against a path; null when it does not apply. */
export function matchPath(pattern: string, path: string): Record<string, string> | null {
  const want = pattern.split("/").filter(Boolean);
  const have = path.split("/").filter(Boolean);
  if (want.length !== have.length) return null;
  const params: Record<string, string> = {};
  for (let i = 0; i < want.length; i++) {
    if (want[i].startsWith(":")) params[want[i].slice(1)] = decodeURIComponent(have[i]);
    else if (want[i] !== have[i]) return null;
  }
  return params;
}

/** Resolves a hash against the route table; falls back to the first route. */
export function resolve(routes: RouteDef[], hash: string): RouteMatch {
  const { path, query } = parseHash(hash);
  for (const r of routes) {
    const params = matchPath(r.pattern, path);
    if (params) return { name: r.name, params, query };
  }
  return { name: routes[0].name, params: {}, query };
}

/** Navigates by mutating the hash (the hashchange listener re-renders). */
export function navigate(path: string): void {
  location.hash = path.startsWith("#") ? path : "#" + path;
}

// The active leave guard: a screen holding unsaved work
// registers one; the shell consults it before applying an in-app hash
// navigation. One guard at a time -- only one screen is mounted.
let leaveGuard: (() => boolean) | null = null;

/** Registers fn as the leave guard; it returns false to block navigation.
 *  Returns the unregister function (idempotent, guard-scoped). */
export function setLeaveGuard(fn: () => boolean): () => void {
  leaveGuard = fn;
  return () => {
    if (leaveGuard === fn) leaveGuard = null;
  };
}

/** True when navigation may proceed: no guard, or the guard consents. */
export function confirmLeave(): boolean {
  return leaveGuard === null || leaveGuard();
}
