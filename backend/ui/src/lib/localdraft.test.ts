// the local draft mirror -- written per edit, offered on mount,
// cleared on save/discard/sign-out -- must round-trip ops and never crash
// on junk storage.
import { beforeEach, describe, expect, it } from "vitest";
import { clearAllLocalDrafts, clearLocalDraft, loadLocalDraft, saveLocalDraft } from "./localdraft";
import type { Op } from "./types";

const op: Op = { resource: "work", path: "tags", action: "add", value: { v: "necromancy" } };

describe("localdraft", () => {
  beforeEach(() => localStorage.clear());

  it("round-trips a draft with its base etag", () => {
    saveLocalDraft("w1", { baseEtag: "e1", ops: [op] });
    const back = loadLocalDraft("w1");
    expect(back?.body.baseEtag).toBe("e1");
    expect(back?.body.ops).toEqual([op]);
    expect(back?.savedAt).toBeTruthy();
    expect(loadLocalDraft("w2")).toBeNull();
  });

  it("an empty op list removes the entry", () => {
    saveLocalDraft("w1", { baseEtag: "e1", ops: [op] });
    saveLocalDraft("w1", { baseEtag: "e1", ops: [] });
    expect(loadLocalDraft("w1")).toBeNull();
  });

  it("junk storage reads as no draft", () => {
    localStorage.setItem("lcat-localdraft-w1", "{not json");
    expect(loadLocalDraft("w1")).toBeNull();
    localStorage.setItem("lcat-localdraft-w1", JSON.stringify({ body: { ops: [] } }));
    expect(loadLocalDraft("w1")).toBeNull();
  });

  it("clearAll sweeps only draft keys (sign-out)", () => {
    saveLocalDraft("w1", { baseEtag: "e1", ops: [op] });
    saveLocalDraft("w2", { baseEtag: "e2", ops: [op] });
    localStorage.setItem("lcat-refresh", "keepme");
    clearAllLocalDrafts();
    expect(loadLocalDraft("w1")).toBeNull();
    expect(loadLocalDraft("w2")).toBeNull();
    expect(localStorage.getItem("lcat-refresh")).toBe("keepme");
  });

  it("clearLocalDraft drops one work only", () => {
    saveLocalDraft("w1", { baseEtag: "e1", ops: [op] });
    saveLocalDraft("w2", { baseEtag: "e2", ops: [op] });
    clearLocalDraft("w1");
    expect(loadLocalDraft("w1")).toBeNull();
    expect(loadLocalDraft("w2")).not.toBeNull();
  });
});
