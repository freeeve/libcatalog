import { describe, expect, it } from "vitest";
import { presentIRIs, wouldChange } from "./subjects";
import type { FieldValue, Op } from "./types";

const SH = "http://id.loc.gov/authorities/subjects/sh93003390";
const HOMO = "https://homosaurus.org/v3/homoit0000075";
const CATS = "http://id.loc.gov/authorities/subjects/sh85021262";

function iri(v: string): FieldValue {
  return { v, iri: true, prov: "editorial", node: "" };
}

function op(action: Op["action"], value?: string): Op {
  return {
    resource: "work",
    path: "subjects",
    action,
    ...(value ? { value: { v: value, iri: true } } : {}),
  };
}

describe("presentIRIs", () => {
  // The reported case: "River of teeth" carries both the homosaurus term and
  // its LCSH equivalent, and the panel offered Add for the LCSH one.
  it("reports the IRIs the record already carries", () => {
    const present = presentIRIs([iri(HOMO), iri(SH)], []);
    expect(present.has(SH)).toBe(true);
    expect(present.has(HOMO)).toBe(true);
    expect(present.has(CATS)).toBe(false);
  });

  it("ignores literal values, which are not crosswalkable terms", () => {
    const present = presentIRIs([{ v: "Bisexual people", prov: "feed", node: "" }], []);
    expect(present.size).toBe(0);
  });

  // A term staged for removal is addable again: the record will not have it.
  it("drops a value staged for removal", () => {
    const present = presentIRIs([iri(SH)], [op("remove", SH)]);
    expect(present.has(SH)).toBe(false);
  });

  it("keeps a value when some other value is staged for removal", () => {
    const present = presentIRIs([iri(SH), iri(HOMO)], [op("remove", HOMO)]);
    expect(present.has(SH)).toBe(true);
    expect(present.has(HOMO)).toBe(false);
  });

  // set and clear redefine the field's whole value set, so nothing stored
  // survives them.
  it("treats set and clear as removing every stored value", () => {
    expect(presentIRIs([iri(SH)], [op("clear")]).size).toBe(0);
    expect(presentIRIs([iri(SH)], [{ resource: "work", path: "subjects", action: "set", values: [{ v: CATS, iri: true }] }]).size).toBe(0);
  });

  it("is empty for a record with no subjects", () => {
    expect(presentIRIs([], []).size).toBe(0);
  });
});

describe("wouldChange", () => {
  it("refuses an add of a term the record already has", () => {
    const present = presentIRIs([iri(SH)], []);
    expect(wouldChange(SH, present)).toBe(false);
  });

  it("allows an add of a genuinely new term", () => {
    const present = presentIRIs([iri(SH)], []);
    expect(wouldChange(CATS, present)).toBe(true);
  });

  it("allows re-adding a term staged for removal", () => {
    const present = presentIRIs([iri(SH)], [op("remove", SH)]);
    expect(wouldChange(SH, present)).toBe(true);
  });
});
