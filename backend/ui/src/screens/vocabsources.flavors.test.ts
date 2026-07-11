import { describe, it, expect } from "vitest";

// the suggest-flavor dropdown here and the Go validator
// (vocabsrc.SuggestFlavors) are two allow-lists. searchfast was added to the
// dispatcher and the builtin `fast` source but neither the validator nor this
// dropdown, so a librarian could not configure it. This pins the dropdown to the
// full set, so dropping (or forgetting to add) a flavor fails the suite.
const files = import.meta.glob("/src/screens/VocabSources.svelte", {
  eager: true,
  query: "?raw",
  import: "default",
}) as Record<string, string>;

describe("VocabSources suggest-flavor dropdown", () => {
  it("offers every configurable suggest flavor", () => {
    const src = Object.values(files)[0];
    expect(src, "VocabSources.svelte not found in the glob").toBeTruthy();
    const sel = src.match(/aria-label="Live typeahead dialect"[\s\S]*?<\/select>/);
    expect(sel, "the suggest-flavor <select> was not found").toBeTruthy();
    const opts = [...sel![0].matchAll(/<option value="([^"]+)">/g)].map((m) => m[1]);
    // Mirrors vocabsrc.SuggestFlavors (Go). Keep in sync when a flavor lands.
    expect(new Set(opts)).toEqual(new Set(["suggest2", "wikidata", "viaf", "searchfast"]));
  });
});
