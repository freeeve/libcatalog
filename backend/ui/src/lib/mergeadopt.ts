// Per-field adoption for the duplicate merge chooser. The
// compare table used to be read-only: a cataloger picked a survivor and then
// re-typed, in its editor, whatever the losing record held. Adoption stages
// that copy as ordinary editor ops against the survivor, applied before the
// merge markers.
import type { FieldValue, Op } from "./types";

/** How many values a field admits: 1 means an adopted value replaces the
 *  survivor's, anything else means it joins. */
export type FieldMax = (path: string) => number | undefined;

/** Whether adopting `theirs` onto `mine` would actually change the survivor.
 *  Offering "adopt" on an identical value would stage an op that writes
 *  nothing, and the cataloger would rightly not trust the next one. */
export function adoptionChanges(mine: FieldValue[], theirs: FieldValue[], max: number | undefined): boolean {
  if (theirs.length === 0) return false;
  if (max === 1) return mine[0]?.v !== theirs[0].v;
  const held = new Set(mine.map((v) => v.v));
  return theirs.some((v) => !held.has(v.v));
}

/** The values an adoption would write: the first for a max-1 field (it
 *  replaces), otherwise only the ones the survivor does not already hold, so
 *  adoption is a union and re-running it is a no-op. */
export function adoptionValues(mine: FieldValue[], theirs: FieldValue[], max: number | undefined): FieldValue[] {
  if (max === 1) return theirs.slice(0, 1);
  const held = new Set(mine.map((v) => v.v));
  return theirs.filter((v) => !held.has(v.v));
}

/** The staged adoptions as editor ops against the survivor, in stable path
 *  order.
 *
 *  A max-1 field becomes one `set` carrying the single value; a repeatable
 *  field becomes one `add` **per** new value, because the ops contract takes
 *  `values` only on `set` and a singular `value` on `add` -- an `add` carrying
 *  an array is refused with "add needs a value".
 *
 *  A field whose adoption turns out to write nothing is dropped rather than
 *  sent: an empty diff in the audit trail is a lie about what the cataloger
 *  did. */
export function adoptionOps(
  adopted: Record<string, string>,
  fieldsOf: (workId: string, path: string) => FieldValue[],
  survivor: string,
  max: FieldMax,
): Op[] {
  const ops: Op[] = [];
  for (const path of Object.keys(adopted).sort()) {
    const from = adopted[path];
    if (!from || from === survivor) continue;
    const chosen = adoptionValues(fieldsOf(survivor, path), fieldsOf(from, path), max(path));
    if (chosen.length === 0) continue;
    const value = (v: FieldValue) => ({ v: v.v, lang: v.lang, iri: v.iri });
    if (max(path) === 1) {
      ops.push({ resource: "work", path, action: "set", values: [value(chosen[0])] });
      continue;
    }
    for (const v of chosen) {
      ops.push({ resource: "work", path, action: "add", value: value(v) });
    }
  }
  return ops;
}
