// Client-side staging for field-level edit operations: the editor form
// accumulates ops locally and the save bar ships them as one
// POST /v1/works/{id}/ops batch. An op is keyed by its full shape
// (resource + path + action + values): staging the identical add/remove
// again toggles it off, while "set" and "clear" define the whole value set
// and so supersede every earlier op staged for the same field.
import { writable, type Readable } from "svelte/store";
import type { Op, OpValue } from "./types";

export interface OpsStore extends Readable<Op[]> {
  /** Stages op. set/clear coalesce per field; an identical add/remove
   *  twice unstages (toggle). */
  stage(op: Op): void;
  unstage(op: Op): void;
  clear(): void;
  /** Replaces the staged list wholesale (draft resume). */
  load(ops: Op[]): void;
  /** The exact wire-shape op list for POST /v1/works/{id}/ops. */
  payload(): Op[];
}

/** Identity of one value within op keys (lang and iri disambiguate). */
export function valueKey(v: OpValue): string {
  return [v.v, v.lang ?? "", v.iri ? "1" : ""].join("\u0001");
}

/** Identity of one op within the staging map. */
export function opKey(op: Op): string {
  const values = op.values ?? (op.value ? [op.value] : []);
  return [op.resource, op.path, op.action, ...values.map(valueKey)].join("\u0000");
}

function fieldKey(op: Op): string {
  return op.resource + "\u0000" + op.path;
}

/** Wire shape for one value: optional fields present only when set. */
function shapeValue(v: OpValue): OpValue {
  const out: OpValue = { v: v.v };
  if (v.lang) out.lang = v.lang;
  if (v.iri) out.iri = true;
  return out;
}

/** Wire shape for one op: value/values present only when carried. */
function shapeWire(op: Op): Op {
  const out: Op = { resource: op.resource, path: op.path, action: op.action };
  if (op.value) out.value = shapeValue(op.value);
  if (op.values) out.values = op.values.map(shapeValue);
  return out;
}

/** A fresh staging store (one per editor session). */
export function createOpsStore(): OpsStore {
  const staged = new Map<string, Op>();
  const { subscribe, set } = writable<Op[]>([]);
  const sync = (): void => set([...staged.values()]);
  return {
    subscribe,
    stage(op: Op): void {
      const shaped = shapeWire(op);
      if (op.action === "set" || op.action === "clear") {
        const fk = fieldKey(op);
        for (const [k, prior] of staged) if (fieldKey(prior) === fk) staged.delete(k);
        staged.set(opKey(shaped), shaped);
      } else {
        const key = opKey(shaped);
        if (staged.has(key)) staged.delete(key);
        else staged.set(key, shaped);
      }
      sync();
    },
    unstage(op: Op): void {
      staged.delete(opKey(shapeWire(op)));
      sync();
    },
    clear(): void {
      staged.clear();
      sync();
    },
    load(ops: Op[]): void {
      staged.clear();
      for (const op of ops) {
        const shaped = shapeWire(op);
        staged.set(opKey(shaped), shaped);
      }
      sync();
    },
    payload(): Op[] {
      return [...staged.values()];
    },
  };
}
