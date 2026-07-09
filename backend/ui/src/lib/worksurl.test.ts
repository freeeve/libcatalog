import { describe, expect, it } from "vitest";
import { parseHash } from "./router";
import { parseWorksQuery, worksHash } from "./worksurl";

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
