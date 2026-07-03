// Staging semantics for field ops: wire shaping, set/clear coalescing per
// field, toggle-off on an identical add/remove, and draft-resume loading.
import { describe, expect, it } from "vitest";
import { get } from "svelte/store";
import { createOpsStore, opKey, valueKey } from "./ops";
import type { Op } from "./types";

function addTag(v: string): Op {
  return { resource: "work", path: "tags", action: "add", value: { v } };
}

function setTitle(v: string): Op {
  return { resource: "work", path: "title", action: "set", values: [{ v }] };
}

describe("op staging", () => {
  it("stages and exposes the wire payload shape", () => {
    const store = createOpsStore();
    store.stage(addTag("sea"));
    expect(get(store)).toHaveLength(1);
    expect(store.payload()).toEqual([{ resource: "work", path: "tags", action: "add", value: { v: "sea" } }]);
    expect(Object.keys(store.payload()[0])).toEqual(["resource", "path", "action", "value"]);
  });

  it("strips unset optional value fields from the wire shape", () => {
    const store = createOpsStore();
    store.stage({ resource: "work", path: "summary", action: "add", value: { v: "x", lang: "", iri: false } });
    expect(store.payload()).toEqual([{ resource: "work", path: "summary", action: "add", value: { v: "x" } }]);
    expect(Object.keys(store.payload()[0].value ?? {})).toEqual(["v"]);
  });

  it("keeps lang and iri when set", () => {
    const store = createOpsStore();
    store.stage({ resource: "work", path: "summary", action: "add", value: { v: "havet", lang: "sv" } });
    store.stage({ resource: "work", path: "language", action: "add", value: { v: "http://l/eng", iri: true } });
    expect(store.payload()).toEqual([
      { resource: "work", path: "summary", action: "add", value: { v: "havet", lang: "sv" } },
      { resource: "work", path: "language", action: "add", value: { v: "http://l/eng", iri: true } },
    ]);
  });

  it("set on set for the same field coalesces to the last set", () => {
    const store = createOpsStore();
    store.stage(setTitle("First"));
    store.stage(setTitle("Second"));
    expect(store.payload()).toEqual([{ resource: "work", path: "title", action: "set", values: [{ v: "Second" }] }]);
  });

  it("set and clear supersede earlier ops on the same field only", () => {
    const store = createOpsStore();
    store.stage(addTag("keeper"));
    store.stage(setTitle("First"));
    store.stage({ resource: "work", path: "title", action: "clear" });
    expect(store.payload()).toEqual([
      { resource: "work", path: "tags", action: "add", value: { v: "keeper" } },
      { resource: "work", path: "title", action: "clear" },
    ]);
  });

  it("the same field on different resources stays apart", () => {
    const store = createOpsStore();
    store.stage({ resource: "i-1", path: "media", action: "set", values: [{ v: "a", iri: true }] });
    store.stage({ resource: "i-2", path: "media", action: "set", values: [{ v: "b", iri: true }] });
    expect(store.payload()).toHaveLength(2);
  });

  it("an identical add staged twice toggles off", () => {
    const store = createOpsStore();
    store.stage(addTag("sea"));
    store.stage(addTag("sea"));
    expect(store.payload()).toEqual([]);
  });

  it("add and remove of the same value are distinct ops", () => {
    const store = createOpsStore();
    store.stage(addTag("sea"));
    store.stage({ resource: "work", path: "tags", action: "remove", value: { v: "sea" } });
    expect(store.payload()).toHaveLength(2);
  });

  it("unstage removes exactly the matching op", () => {
    const store = createOpsStore();
    store.stage(addTag("sea"));
    store.stage(addTag("ships"));
    store.unstage(addTag("sea"));
    expect(store.payload()).toEqual([{ resource: "work", path: "tags", action: "add", value: { v: "ships" } }]);
    store.clear();
    expect(get(store)).toEqual([]);
  });

  it("load replaces the staged list wholesale (draft resume)", () => {
    const store = createOpsStore();
    store.stage(addTag("stale"));
    store.load([setTitle("Restored"), addTag("resumed")]);
    expect(store.payload()).toEqual([
      { resource: "work", path: "title", action: "set", values: [{ v: "Restored" }] },
      { resource: "work", path: "tags", action: "add", value: { v: "resumed" } },
    ]);
  });

  it("keys distinguish value, lang, and iri", () => {
    expect(valueKey({ v: "x" })).not.toBe(valueKey({ v: "x", lang: "en" }));
    expect(valueKey({ v: "x" })).not.toBe(valueKey({ v: "x", iri: true }));
    expect(valueKey({ v: "x", lang: "" })).toBe(valueKey({ v: "x" }));
    expect(opKey(addTag("a"))).not.toBe(opKey(addTag("b")));
    expect(opKey(addTag("a"))).not.toBe(opKey({ resource: "work", path: "tags", action: "remove", value: { v: "a" } }));
  });
});
