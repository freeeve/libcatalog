import { describe, expect, it } from "vitest";
import { parseHash } from "./router";
import { exportsHash, parseExportFacets, parseWorksQuery, worksHash } from "./worksurl";

const params = (hash: string) => parseHash(hash).query;

describe("worksHash", () => {
  it("renders a plain hash for the empty state", () => {
    expect(worksHash("", {})).toBe("#/works");
    expect(worksHash("   ", {})).toBe("#/works");
  });

  it("carries the query and repeated filter params in group-sorted order", () => {
    expect(worksHash("lesbian", { needs: ["subjects"], holdings: ["none"] })).toBe(
      "#/works?q=lesbian&holdings=none&needs=subjects",
    );
    expect(worksHash("", { sources: ["loc", "mombian"] })).toBe("#/works?sources=loc&sources=mombian");
  });

  it("encodes reserved characters round-trip", () => {
    const h = worksHash("a&b=c", { subject: ["https://homosaurus.org/v4/homoit0001235"] });
    const back = parseWorksQuery(parseHash(h).query);
    expect(back).toEqual({ q: "a&b=c", filters: { subject: ["https://homosaurus.org/v4/homoit0001235"] } });
  });
});

describe("parseWorksQuery", () => {
  it("returns null when the URL carries no state", () => {
    expect(parseWorksQuery(parseHash("#/works").query)).toBeNull();
  });

  it("splits q from filter groups and keeps repeated values", () => {
    const q = parseHash("#/works?q=lesbian&holdings=none&sources=a&sources=b").query;
    expect(parseWorksQuery(q)).toEqual({ q: "lesbian", filters: { holdings: ["none"], sources: ["a", "b"] } });
  });

  it("treats a filters-only URL as state with an empty query", () => {
    const q = parseHash("#/works?needs=subjects").query;
    expect(parseWorksQuery(q)).toEqual({ q: "", filters: { needs: ["subjects"] } });
  });

  it("round-trips through worksHash canonically", () => {
    const state = parseWorksQuery(parseHash("#/works?needs=subjects&q=x&holdings=none").query);
    expect(worksHash(state!.q, state!.filters)).toBe("#/works?q=x&holdings=none&needs=subjects");
  });
});

// "Export these results…" carried only the query. With facets applied
// it degraded to kind=all, so a cataloger who had narrowed 62,602 works to 465
// was handed the whole catalog, preloaded and previewed as if they had asked.
describe("exportsHash", () => {
  it("carries the facet filters, not just the query", () => {
    const p = params(exportsHash("lesbian", { holdings: ["none"], tag: ["poetry"] }, false));

    expect(p.get("kind")).toBe("search");
    expect(p.get("q")).toBe("lesbian");
    expect(p.getAll("f.holdings")).toEqual(["none"]);
    expect(p.getAll("f.tag")).toEqual(["poetry"]);
  });

  // A search selection with no query is refused by the server, so
  // "everything, filtered" has to be kind=all plus facets.
  it("uses kind=all when there is no query, and still carries the facets", () => {
    const p = params(exportsHash("   ", { tag: ["poetry"] }, false));

    expect(p.get("kind")).toBe("all");
    expect(p.get("q")).toBeNull();
    expect(p.getAll("f.tag")).toEqual(["poetry"]);
  });

  it("keeps multiple values of one group", () => {
    expect(params(exportsHash("", { sources: ["mombian", "queer lit"] }, false)).getAll("f.sources")).toEqual([
      "mombian",
      "queer lit",
    ]);
  });

  // The works screen hides retired records by default; the export
  // must agree or the preview count will not match the count beside the link.
  it("mirrors the works screen's tombstoned mode", () => {
    expect(params(exportsHash("", {}, false)).get("tombstoned")).toBe("exclude");
    expect(params(exportsHash("", {}, true)).get("tombstoned")).toBeNull();
  });

  // The prefix keeps a facet group named "kind" or "q" from overwriting the
  // exports screen's own params.
  it("namespaces facets so they cannot collide with kind/q/ids/sq", () => {
    const p = params(exportsHash("real query", { kind: ["ids"], q: ["hijack"] }, true));

    expect(p.get("kind")).toBe("search");
    expect(p.get("q")).toBe("real query");
    expect(p.getAll("f.kind")).toEqual(["ids"]);
    expect(p.getAll("f.q")).toEqual(["hijack"]);
  });
});

describe("parseExportFacets", () => {
  it("round-trips what exportsHash wrote", () => {
    const filters = { holdings: ["none"], sources: ["mombian", "queer lit"] };
    expect(parseExportFacets(params(exportsHash("x", filters, false)))).toEqual(filters);
  });

  it("ignores the exports screen's own params", () => {
    expect(parseExportFacets(parseHash("#/exports?kind=search&q=poetry&ids=w1&sq=s1&tombstoned=exclude").query)).toEqual({});
  });

  it("ignores a bare prefix with no group name", () => {
    expect(parseExportFacets(parseHash("#/exports?f.=x").query)).toEqual({});
  });
});
