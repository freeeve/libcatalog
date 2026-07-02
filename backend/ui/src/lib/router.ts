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
