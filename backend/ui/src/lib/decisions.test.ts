// Staging semantics for review decisions: replace-by-identity, toggle-off on
// an identical repeat, and the exact wire payload for POST /v1/review.
import { describe, expect, it } from "vitest";
import { get } from "svelte/store";
import { createDecisionStore, decisionKey } from "./decisions";
import type { Decision, TermRef } from "./types";

const term: TermRef = { scheme: "lcsh", id: "http://id.loc.gov/sh1", label: "Sea monsters" };
const otherTerm: TermRef = { scheme: "fast", id: "fst-1", label: "Krakens" };

function approve(workId = "w1", t: TermRef = term): Decision {
  return { workId, term: t, type: "ADD", approve: true };
}

describe("decision staging", () => {
  it("stages and exposes the wire payload shape", () => {
    const store = createDecisionStore();
    store.stage(approve());
    expect(get(store)).toHaveLength(1);
    expect(store.payload()).toEqual([{ workId: "w1", term, type: "ADD", approve: true }]);
    // Optional fields never appear as explicit keys when unset.
    expect(Object.keys(store.payload()[0])).toEqual(["workId", "term", "type", "approve"]);
  });

  it("a different action for the same suggestion replaces the entry", () => {
    const store = createDecisionStore();
    store.stage(approve());
    store.stage({ workId: "w1", term, type: "ADD", approve: false });
    expect(store.payload()).toEqual([{ workId: "w1", term, type: "ADD", approve: false }]);
  });

  it("repeating the identical action toggles it off", () => {
    const store = createDecisionStore();
    store.stage(approve());
    store.stage(approve());
    expect(get(store)).toEqual([]);
    expect(store.payload()).toEqual([]);
  });

  it("keeps distinct suggestions apart", () => {
    const store = createDecisionStore();
    store.stage(approve("w1"));
    store.stage(approve("w2"));
    store.stage(approve("w1", otherTerm));
    expect(store.payload()).toHaveLength(3);
    expect(decisionKey("w1", term, "ADD")).not.toBe(decisionKey("w1", otherTerm, "ADD"));
    expect(decisionKey("w1", term, "ADD")).not.toBe(decisionKey("w1", term, "REMOVE"));
  });

  it("carries tombstone and note on rejections", () => {
    const store = createDecisionStore();
    store.stage({ workId: "w1", term, type: "ADD", approve: false, tombstone: true, note: "never again" });
    expect(store.payload()).toEqual([
      { workId: "w1", term, type: "ADD", approve: false, tombstone: true, note: "never again" },
    ]);
  });

  it("distinguishes a plain reject from reject + tombstone when toggling", () => {
    const store = createDecisionStore();
    store.stage({ workId: "w1", term, type: "ADD", approve: false });
    store.stage({ workId: "w1", term, type: "ADD", approve: false, tombstone: true });
    expect(store.payload()).toEqual([{ workId: "w1", term, type: "ADD", approve: false, tombstone: true }]);
  });

  it("carries a substitute term and toggles only on the same substitute", () => {
    const store = createDecisionStore();
    store.stage({ workId: "w1", term, type: "ADD", approve: true, substituteTerm: otherTerm });
    // Approving with a different substitute replaces, not toggles.
    const third: TermRef = { scheme: "fast", id: "fst-2", label: "Leviathans" };
    store.stage({ workId: "w1", term, type: "ADD", approve: true, substituteTerm: third });
    expect(store.payload()).toEqual([{ workId: "w1", term, type: "ADD", approve: true, substituteTerm: third }]);
    // The identical substitute approval toggles off.
    store.stage({ workId: "w1", term, type: "ADD", approve: true, substituteTerm: third });
    expect(store.payload()).toEqual([]);
  });

  it("unstage removes one entry and clear empties the store", () => {
    const store = createDecisionStore();
    store.stage(approve("w1"));
    store.stage(approve("w2"));
    store.unstage("w1", term, "ADD");
    expect(store.payload().map((d) => d.workId)).toEqual(["w2"]);
    store.clear();
    expect(get(store)).toEqual([]);
  });

  it("persists staged decisions to sessionStorage and hydrates a new store", () => {
    sessionStorage.clear();
    const store = createDecisionStore("test.decisions");
    store.stage(approve("w1"));
    store.stage(approve("w2"));
    const revived = createDecisionStore("test.decisions");
    expect(revived.payload().map((d) => d.workId).sort()).toEqual(["w1", "w2"]);
    revived.clear();
    expect(sessionStorage.getItem("test.decisions")).toBeNull();
    sessionStorage.clear();
  });

  it("hydration tolerates corrupt storage", () => {
    sessionStorage.setItem("test.corrupt", "{not json");
    const store = createDecisionStore("test.corrupt");
    expect(store.payload()).toEqual([]);
    sessionStorage.clear();
  });

  // The zombie fix (task 444): a persisted decision whose row was resolved
  // elsewhere must drop on reconcile -- kept, it re-reported "already
  // decided" on every apply forever -- while decisions for still-open rows
  // survive, and the persisted mirror follows.
  it("reconcile drops decisions for resolved rows and reports them", () => {
    sessionStorage.clear();
    const store = createDecisionStore("test.reconcile");
    store.stage(approve("w1"));
    store.stage(approve("w2", otherTerm));
    const open = new Set([decisionKey("w2", otherTerm, "ADD")]);
    const dropped = store.reconcile(open);
    expect(dropped.map((d) => d.workId)).toEqual(["w1"]);
    expect(store.payload().map((d) => d.workId)).toEqual(["w2"]);
    const revived = createDecisionStore("test.reconcile");
    expect(revived.payload().map((d) => d.workId)).toEqual(["w2"]);
    sessionStorage.clear();
  });

  it("reconcile with every row open drops nothing and skips the sync", () => {
    const store = createDecisionStore();
    store.stage(approve("w1"));
    const dropped = store.reconcile(new Set([decisionKey("w1", term, "ADD")]));
    expect(dropped).toEqual([]);
    expect(store.payload()).toHaveLength(1);
  });
});
