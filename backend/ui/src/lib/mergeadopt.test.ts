import { describe, expect, it } from "vitest";
import { adoptionChanges, adoptionOps, adoptionValues } from "./mergeadopt";
import type { FieldValue } from "./types";

const v = (s: string, extra: Partial<FieldValue> = {}): FieldValue => ({ v: s, ...extra }) as FieldValue;

describe("adoptionChanges", () => {
  it("says no when the loser has nothing to give", () => {
    expect(adoptionChanges([v("a")], [], 1)).toBe(false);
    expect(adoptionChanges([], [], undefined)).toBe(false);
  });

  it("says no when a max-1 field already holds that value", () => {
    expect(adoptionChanges([v("Frog and Toad")], [v("Frog and Toad")], 1)).toBe(false);
    expect(adoptionChanges([v("Frog and Toad")], [v("Frog & Toad")], 1)).toBe(true);
  });

  it("says no when a repeatable field already holds every value", () => {
    expect(adoptionChanges([v("a"), v("b")], [v("b")], undefined)).toBe(false);
    expect(adoptionChanges([v("a")], [v("a"), v("c")], undefined)).toBe(true);
  });

  it("fills an empty survivor field either way", () => {
    expect(adoptionChanges([], [v("x")], 1)).toBe(true);
    expect(adoptionChanges([], [v("x")], undefined)).toBe(true);
  });
});

describe("adoptionValues", () => {
  it("takes only the first value of a max-1 field", () => {
    expect(adoptionValues([v("old")], [v("new"), v("extra")], 1)).toEqual([v("new")]);
  });

  it("unions a repeatable field, dropping what the survivor holds", () => {
    expect(adoptionValues([v("a")], [v("a"), v("b")], undefined)).toEqual([v("b")]);
  });
});

// adoption stages ordinary editor ops against the survivor,
// so the merge chooser writes through the same audited path as the editor.
describe("adoptionOps", () => {
  type Docs = Record<string, Record<string, FieldValue[]>>;
  const lookup =
    (d: Docs) =>
    (workId: string, path: string): FieldValue[] =>
      d[workId]?.[path] ?? [];

  const docs: Docs = {
    surv: { title: [v("Frog and Toad")], tags: [v("classic")], summary: [] },
    loser: {
      title: [v("Frog and Toad Together")],
      tags: [v("classic"), v("beginner")],
      summary: [v("Five stories", { lang: "en" })],
    },
  };
  const fieldsOf = lookup(docs);
  const max = (path: string) => (path === "title" || path === "summary" ? 1 : undefined);

  it("sets a max-1 field and adds only the new values of a repeatable one", () => {
    const ops = adoptionOps({ title: "loser", tags: "loser" }, fieldsOf, "surv", max);
    expect(ops).toEqual([
      { resource: "work", path: "tags", action: "add", value: { v: "beginner", lang: undefined, iri: undefined } },
      {
        resource: "work",
        path: "title",
        action: "set",
        values: [{ v: "Frog and Toad Together", lang: undefined, iri: undefined }],
      },
    ]);
  });

  // The ops contract takes `values` only on `set`; an `add` carrying an array
  // is refused server-side with "add needs a value".
  it("emits one add op per value, never an array", () => {
    const many: Docs = { surv: { tags: [] }, loser: { tags: [v("a"), v("b")] } };
    const ops = adoptionOps({ tags: "loser" }, lookup(many), "surv", max);
    expect(ops).toHaveLength(2);
    for (const op of ops) {
      expect(op.action).toBe("add");
      expect(op.values).toBeUndefined();
      expect(op.value).toBeDefined();
    }
    expect(ops.map((o) => o.value?.v)).toEqual(["a", "b"]);
  });

  it("carries the language tag across", () => {
    const ops = adoptionOps({ summary: "loser" }, fieldsOf, "surv", max);
    expect(ops[0].values).toEqual([{ v: "Five stories", lang: "en", iri: undefined }]);
  });

  it("drops an adoption that would write nothing", () => {
    // The survivor already holds "classic", and nothing else was adopted.
    const same: Docs = { surv: { tags: [v("classic")] }, loser: { tags: [v("classic")] } };
    const ops = adoptionOps({ tags: "loser" }, lookup(same), "surv", max);
    expect(ops).toEqual([]);
  });

  it("ignores an adoption pointing at the survivor itself", () => {
    expect(adoptionOps({ title: "surv" }, fieldsOf, "surv", max)).toEqual([]);
  });

  it("emits ops in stable path order", () => {
    const ops = adoptionOps({ title: "loser", tags: "loser" }, fieldsOf, "surv", max);
    expect(ops.map((o) => o.path)).toEqual(["tags", "title"]);
  });

  it("counts fields, not ops, so the UI's staged count stays truthful", () => {
    const many: Docs = { surv: { tags: [] }, loser: { tags: [v("a"), v("b"), v("c")] } };
    const ops = adoptionOps({ tags: "loser" }, lookup(many), "surv", max);
    expect(new Set(ops.map((o) => o.path)).size).toBe(1);
    expect(ops).toHaveLength(3);
  });
});
