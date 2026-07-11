// deleteItemTemplate was exported, documented, and called by
// nobody -- a half-wired feature no check noticed. This guards the class: an
// exported api.ts function with zero references anywhere else in src fails the
// suite the day it lands, instead of shipping dead.
import { describe, it, expect } from "vitest";

// Every source file's raw text, keyed by path. import.meta.glob keeps this to
// Vite's own machinery -- no node fs, no @types/node in the check tsconfig.
const files = import.meta.glob("/src/**/*.{ts,svelte}", {
  eager: true,
  query: "?raw",
  import: "default",
}) as Record<string, string>;

describe("api.ts exports", () => {
  it("has no exported function without a caller", () => {
    const apiKey = Object.keys(files).find((k) => k.endsWith("/lib/api.ts"));
    expect(apiKey, "api.ts not found in the source glob").toBeTruthy();
    const api = files[apiKey as string];
    const names = [...api.matchAll(/export (?:async )?function ([A-Za-z0-9_]+)/g)].map((m) => m[1]);
    expect(names.length).toBeGreaterThan(50); // sanity: the regex is finding them

    // Every other source file, tests included: a function exercised only by a
    // test still has a caller; a truly dead export appears in api.ts alone.
    const corpus = Object.entries(files)
      .filter(([k]) => k !== apiKey)
      .map(([, text]) => text)
      .join("\n");

    const dead = names.filter((n) => !new RegExp(`\\b${n}\\b`).test(corpus));
    expect(dead, `dead api exports (delete them or wire them): ${dead.join(", ")}`).toEqual([]);
  });
});
