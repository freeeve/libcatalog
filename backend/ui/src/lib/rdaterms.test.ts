import { describe, expect, it } from "vitest";
import { CARRIER_TYPES, ISSUANCE_TYPES, MEDIA_TYPES, rdaTerm } from "./rdaterms";

describe("rdaterms", () => {
  it("labels the LOC media and carrier IRIs the crosswalk emits", () => {
    expect(rdaTerm("http://id.loc.gov/vocabulary/carriers/cr")?.label).toBe("online resource");
    expect(rdaTerm("http://id.loc.gov/vocabulary/carriers/sz")?.label).toBe("other audio carrier");
    expect(rdaTerm("http://id.loc.gov/vocabulary/mediaTypes/c")?.label).toBe("computer");
    expect(rdaTerm("http://id.loc.gov/vocabulary/mediaTypes/s")?.label).toBe("audio");
  });

  it("labels the LOC issuance IRIs the crosswalk emits", () => {
    expect(rdaTerm("http://id.loc.gov/vocabulary/issuance/mono")?.label).toBe("single unit");
    expect(rdaTerm("http://id.loc.gov/vocabulary/issuance/serl")?.label).toBe("serial");
    expect(rdaTerm("http://id.loc.gov/vocabulary/issuance/mulm")?.label).toBe("multipart monograph");
    expect(rdaTerm("http://id.loc.gov/vocabulary/issuance/intg")?.label).toBe("integrating resource");
  });

  it("returns undefined for unknown IRIs (generic display)", () => {
    expect(rdaTerm("http://rdaregistry.info/termList/RDAMediaType/1003")).toBeUndefined();
    expect(rdaTerm("not-an-iri")).toBeUndefined();
  });

  it("ships closed, unique lists with codes", () => {
    const all = [...MEDIA_TYPES, ...CARRIER_TYPES, ...ISSUANCE_TYPES];
    expect(MEDIA_TYPES).toHaveLength(10);
    expect(new Set(all.map((t) => t.iri)).size).toBe(all.length);
    for (const t of all) {
      expect(t.iri.endsWith("/" + t.code)).toBe(true);
      expect(t.label).not.toBe("");
    }
  });
});
