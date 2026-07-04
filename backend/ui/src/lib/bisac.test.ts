import { describe, expect, it } from "vitest";
import { bisacTerm } from "./bisac";

describe("bisacTerm", () => {
  it("decodes a general code to its section heading", () => {
    expect(bisacTerm("DRA000000")).toEqual({ label: "Drama / General", code: "DRA000000", exact: true });
  });

  it("falls back to the section name for specific subheadings", () => {
    expect(bisacTerm("FIC027000")).toEqual({ label: "Fiction", code: "FIC027000", exact: false });
  });

  it("normalizes case and whitespace", () => {
    expect(bisacTerm(" juv000000 ")?.label).toBe("Juvenile Fiction / General");
  });

  it("rejects unknown sections and non-BISAC strings", () => {
    expect(bisacTerm("ZZZ000000")).toBeUndefined();
    expect(bisacTerm("813.54")).toBeUndefined();
    expect(bisacTerm("")).toBeUndefined();
  });
});
