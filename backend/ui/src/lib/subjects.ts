// Which IRI values a record's field already carries.
//
// The subject neighborhood exists to crosswalk a term onto a record -- pull the
// LCSH equivalent onto a record that has the homosaurus term. Records where a
// previous cataloger already did that are exactly the records where the panel
// is most likely to be opened, and there its headline action is a no-op dressed
// as an edit: it stages an add for a subject the record already has, the value
// renders twice, the editor goes dirty, and saving writes a history entry for
// an edit that changes nothing.
//
// A term counts as present when the record holds it and nothing staged removes
// it. A staged removal makes the term addable again -- "drop it and re-add it"
// is a real, if unusual, thing a cataloger may be mid-way through.
import type { FieldValue, Op } from "./types";
import { valueKey } from "./ops";

/** True when this op takes value away from the field: an explicit remove of the
 *  same value, or a set/clear that redefines the field's whole value set. */
function removes(op: Op, fv: FieldValue): boolean {
  if (op.action === "set" || op.action === "clear") return true;
  if (op.action !== "remove" || !op.value) return false;
  return valueKey(op.value) === valueKey({ v: fv.v, lang: fv.lang, iri: fv.iri });
}

/**
 * The set of IRI values the field currently holds, after staged removals.
 *
 * @param values the field's stored values
 * @param fieldOps the ops staged against this same field
 */
export function presentIRIs(values: FieldValue[], fieldOps: Op[]): Set<string> {
  const present = new Set<string>();
  for (const fv of values) {
    if (!fv.iri) continue;
    if (fieldOps.some((op) => removes(op, fv))) continue;
    present.add(fv.v);
  }
  return present;
}

/**
 * Whether staging an "add" of iri onto the field would change the record.
 *
 * @param iri the term being added
 * @param present the field's current IRI values, from {@link presentIRIs}
 */
export function wouldChange(iri: string, present: Set<string>): boolean {
  return !present.has(iri);
}
