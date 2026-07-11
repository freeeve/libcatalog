// Client-side macro replay: ${name} parameter substitution over an op list,
// mirroring the server's batch.ApplyParams so a macro means the same thing
// replayed in the editor or run over a selection.
import type { Macro, Op, OpValue } from "./types";

const PARAM_REF = /\$\{([A-Za-z0-9_-]+)\}/g;

/** True when the macro's op values reference at least one parameter. */
export function hasParams(m: Macro): boolean {
  return (m.params ?? []).length > 0;
}

/** Substitutes ${name} references from values (falling back to declared
 *  defaults) and returns the concrete op list. Throws on an unresolved
 *  reference -- a macro never silently writes its placeholder text. */
export function applyParams(m: Macro, values: Record<string, string>): Op[] {
  const lookup: Record<string, string> = {};
  for (const p of m.params ?? []) {
    if (p.default) lookup[p.name] = p.default;
  }
  for (const [name, v] of Object.entries(values)) {
    if (v !== "") lookup[name] = v;
  }
  const subst = (raw: string): string =>
    raw.replace(PARAM_REF, (_, name: string) => {
      const v = lookup[name];
      if (v === undefined) throw new Error(`parameter "${name}" has no value`);
      return v;
    });
  const substValue = (v: OpValue): OpValue => ({ ...v, v: subst(v.v) });
  return m.ops.map((op) => ({
    ...op,
    ...(op.value ? { value: substValue(op.value) } : {}),
    ...(op.values ? { values: op.values.map(substValue) } : {}),
  }));
}
