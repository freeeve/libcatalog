import { describe, expect, it } from "vitest";
import { matchPath, parseHash, resolve, type RouteDef } from "./router";

const routes: RouteDef[] = [
  { name: "dashboard", pattern: "/" },
  { name: "login", pattern: "/login" },
  { name: "works", pattern: "/works" },
  { name: "work", pattern: "/works/:id" },
  { name: "queue", pattern: "/queue" },
];

describe("parseHash", () => {
  it("defaults the empty hash to /", () => {
    expect(parseHash("").path).toBe("/");
    expect(parseHash("#").path).toBe("/");
    expect(parseHash("#/").path).toBe("/");
  });

  it("splits path and query", () => {
    const { path, query } = parseHash("#/works?q=sea&limit=10");
    expect(path).toBe("/works");
    expect(query.get("q")).toBe("sea");
    expect(query.get("limit")).toBe("10");
  });
});

describe("matchPath", () => {
  it("matches literal segments", () => {
    expect(matchPath("/works", "/works")).toEqual({});
    expect(matchPath("/works", "/queue")).toBeNull();
  });

  it("captures and decodes params", () => {
    expect(matchPath("/works/:id", "/works/w%2F1")).toEqual({ id: "w/1" });
  });

  it("requires equal segment counts", () => {
    expect(matchPath("/works/:id", "/works")).toBeNull();
    expect(matchPath("/works", "/works/w1")).toBeNull();
  });
});

describe("resolve", () => {
  it("routes deep links with params and query", () => {
    const m = resolve(routes, "#/works/abc?tab=fields");
    expect(m.name).toBe("work");
    expect(m.params.id).toBe("abc");
    expect(m.query.get("tab")).toBe("fields");
  });

  it("falls back to the first route for unknown paths", () => {
    expect(resolve(routes, "#/nope/deeper").name).toBe("dashboard");
  });
});
