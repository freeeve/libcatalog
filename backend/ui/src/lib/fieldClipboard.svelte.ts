// App-level MARC field clipboard: cut/copied fields accumulate
// newest-first and paste back into any editing surface (grid now, text mode
//). Module $state so every consumer shares one clipboard;
// deliberately not persisted -- fields are working material, not documents.
import type { MarcField } from "./types";

const MAX_ENTRIES = 20;

export const fieldClipboard = $state<{ entries: MarcField[] }>({ entries: [] });

/** Adds a field to the top of the clipboard (a plain deep copy, so later
 *  grid edits cannot mutate it). Snapshot first: callers may hand a $state
 * proxy, which structuredClone cannot clone. */
export function clipPush(f: MarcField): void {
  fieldClipboard.entries = [structuredClone($state.snapshot(f)) as MarcField, ...fieldClipboard.entries].slice(0, MAX_ENTRIES);
}

/** The most recent entry, copied for insertion; undefined when empty.
 *  Reading entries back through the module $state re-proxies them, so the
 * copy must snapshot before cloning. */
export function clipPeek(): MarcField | undefined {
  return clipAt(0);
}

/** A specific entry, copied for insertion (the pane picks). */
export function clipAt(i: number): MarcField | undefined {
  const f = fieldClipboard.entries[i];
  return f ? (structuredClone($state.snapshot(f)) as MarcField) : undefined;
}

/** Removes one entry. */
export function clipRemove(i: number): void {
  fieldClipboard.entries = fieldClipboard.entries.filter((_, j) => j !== i);
}

/** Empties the clipboard. */
export function clipClear(): void {
  fieldClipboard.entries = [];
}
