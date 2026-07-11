// the record editor now takes its field set, order, labels, and hidden
// flags from the deployment's editing profile, merged onto the local presentation
// table by path. These tests pin the merge logic and guard the presentation from
// drifting out from under the shipped work profile.
import { describe, expect, it } from "vitest";
import workMonograph from "../../../profiles/defaults/work-monograph.json";
import instanceEbook from "../../../profiles/defaults/instance-ebook.json";
import { INSTANCE_FIELDS, WORK_FIELDS, buildFieldSpecs } from "./profileSpecs";
import type { ProfileField } from "../lib/types";

describe("buildFieldSpecs", () => {
  const presentation = [
    { path: "title", label: "Title", kind: "single" as const },
    { path: "tags", label: "Tags", kind: "tag" as const, section: "more" as const },
  ];

  it("takes field set, order, labels, and hidden flags from the profile", () => {
    const fields: ProfileField[] = [
      { path: "tags", label: "Keywords" }, // renamed + reordered before title
      { path: "title", label: "Heading" },
    ];
    const specs = buildFieldSpecs(presentation, fields);
    expect(specs.map((s) => s.path)).toEqual(["tags", "title"]); // profile order
    expect(specs.map((s) => s.label)).toEqual(["Keywords", "Heading"]); // profile labels
    // Presentation (kind, section) survives the merge, keyed by path.
    expect(specs[0].kind).toBe("tag");
    expect(specs[0].section).toBe("more");
  });

  it("drops hidden fields", () => {
    const specs = buildFieldSpecs(presentation, [
      { path: "title", label: "Title" },
      { path: "tags", label: "Tags", hidden: true },
    ]);
    expect(specs.map((s) => s.path)).toEqual(["title"]);
  });

  it("renders a profile field with no presentation entry, deriving its kind from the value source", () => {
    const specs = buildFieldSpecs(presentation, [{ path: "newField", label: "New", valueSource: { kind: "vocab" } }]);
    expect(specs).toHaveLength(1);
    expect(specs[0]).toMatchObject({ path: "newField", label: "New", kind: "vocab" });
  });

  it("falls back to the shipped presentation when no profile is passed", () => {
    expect(buildFieldSpecs(presentation, undefined)).toBe(presentation);
    expect(buildFieldSpecs(presentation, [])).toBe(presentation);
  });
});

describe("WORK_FIELDS presentation", () => {
  it("covers every field the shipped work-monograph profile declares", () => {
    // A shipped profile field with no presentation entry would render with a
    // derived kind and no options/decode/section -- a silent downgrade. Pin the
    // presentation to the shipped profile so adding a field to one flags the other.
    const presented = new Set(WORK_FIELDS.map((s) => s.path));
    const missing = (workMonograph.fields as { path: string }[]).map((f) => f.path).filter((p) => !presented.has(p));
    expect(missing, `work-monograph paths lacking a presentation entry: ${missing.join(", ")}`).toEqual([]);
  });
});

describe("INSTANCE_FIELDS presentation", () => {
  it("covers every field the shipped instance-ebook profile declares", () => {
    // instance-ebook.json declared series/seriesEnumeration that the hand-copied
    // presentation never had -- exactly the drift predicted.
    // Pin the presentation to the shipped profile so it cannot recur.
    const presented = new Set(INSTANCE_FIELDS.map((s) => s.path));
    const missing = (instanceEbook.fields as { path: string }[]).map((f) => f.path).filter((p) => !presented.has(p));
    expect(missing, `instance-ebook paths lacking a presentation entry: ${missing.join(", ")}`).toEqual([]);
  });
});
