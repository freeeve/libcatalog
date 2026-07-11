// Item ops for the batch screen. Holdings used to be
// reachable only one record at a time, through the editor's item panel;
// `resource: "items"` lets a batch run reshelve a selection through the same
// audited op list, with the same dry-run diff.
import { RESOURCE_ITEMS, type Op, type OpAction } from "./types";

/** The item fields a batch may edit. Mirrors bibframe.ItemFieldNames, and
 *  `TestItemFieldsMatchUI` fails if the two drift.
 *
 *  Barcode is absent on purpose: it names one physical copy, so assigning it
 *  across a selection would mint duplicates. */
export const ITEM_FIELDS: { path: string; label: string }[] = [
  { path: "callNumber", label: "Call number" },
  { path: "location", label: "Shelving location" },
  { path: "note", label: "Item note" },
];

/** Which items an edit reaches. `all` is every copy; `eq` is the ones whose
 *  current value matches exactly (the relocation case: Stacks to Annex leaves
 *  Reference alone); `empty` is the ones missing the field. */
export type ItemGuard = "all" | "eq" | "empty";

/** A field select's option value, encoding the resource it belongs to. Work
 *  fields and item fields share one picker, so the row has to remember which
 *  kind it chose. */
export function fieldKey(resource: string, path: string): string {
  return `${resource}:${path}`;
}

export function parseFieldKey(key: string): { resource: string; path: string } {
  const at = key.indexOf(":");
  if (at < 0) return { resource: "work", path: key };
  return { resource: key.slice(0, at), path: key.slice(at + 1) };
}

export function isItemField(key: string): boolean {
  return parseFieldKey(key).resource === RESOURCE_ITEMS;
}

/** An item field holds one value, so only these two actions mean anything;
 *  the server refuses add/remove rather than reinterpreting them. */
export const ITEM_ACTIONS: OpAction[] = ["set", "clear"];

/** The guard as the wire sends it: `undefined` for every item, otherwise the
 *  exact current value to match. An absent field reads as "", which is what
 *  makes `empty` a plain guard rather than a special case. */
export function guardValue(guard: ItemGuard, where: string): string | undefined {
  if (guard === "all") return undefined;
  if (guard === "empty") return "";
  return where;
}

/** One item-field row as an op, or null when the row is not yet runnable: a
 *  `set` with no value would clear the field, which is what `clear` is for. */
export function itemOp(path: string, action: OpAction, value: string, guard: ItemGuard, where: string): Op | null {
  if (!path) return null;
  const g = guardValue(guard, where);
  if (action === "clear") {
    return { resource: RESOURCE_ITEMS, path, action, ...(g === undefined ? {} : { where: g }) };
  }
  if (action !== "set" || !value) return null;
  return { resource: RESOURCE_ITEMS, path, action: "set", values: [{ v: value }], ...(g === undefined ? {} : { where: g }) };
}

/** How the run summary describes an item op, so the cataloger reads the reach
 *  of the edit before running it rather than after. */
export function itemOpSummary(op: Op): string {
  const field = ITEM_FIELDS.find((f) => f.path === op.path)?.label ?? op.path;
  const scope = op.where === undefined ? "every item" : op.where === "" ? "items with no " + field.toLowerCase() : `items where ${field.toLowerCase()} is “${op.where}”`;
  if (op.action === "clear") return `clear ${field.toLowerCase()} on ${scope}`;
  return `set ${field.toLowerCase()} to “${op.values?.[0]?.v ?? ""}” on ${scope}`;
}
