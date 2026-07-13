// Client-side staging for review decisions: queue rows accumulate
// approve/reject choices locally and the publish bar ships them as one
// POST /v1/review batch. A decision is keyed by suggestion identity
// (work + term + type): staging a different action replaces the entry,
// re-staging the identical action toggles it off.
import { writable, type Readable } from "svelte/store";
import type { Decision, SuggType, TermRef } from "./types";

export interface DecisionStore extends Readable<Decision[]> {
  /** Stages d, replacing any prior decision for the same suggestion; the
   *  identical action twice unstages (toggle). */
  stage(d: Decision): void;
  unstage(workId: string, term: TermRef, type: SuggType): void;
  clear(): void;
  /** The exact wire-shape decision list for POST /v1/review. */
  payload(): Decision[];
  /** Drops staged decisions whose suggestion key is absent from the open
   *  set (rows resolved elsewhere -- another tab, an earlier apply) and
   *  returns the dropped ones so the screen can say so. Without this a
   *  persisted decision for a resolved row was an invisible zombie:
   *  staged forever, re-reported "already decided" on every apply. */
  reconcile(openKeys: Set<string>): Decision[];
}

/** Identity of one suggestion within the staging map. */
export function decisionKey(workId: string, term: TermRef, type: SuggType): string {
  return [workId, term.scheme, term.id, type].join("\u0000");
}

function sameAction(a: Decision, b: Decision): boolean {
  return (
    a.approve === b.approve &&
    !!a.tombstone === !!b.tombstone &&
    (a.substituteTerm?.scheme ?? "") === (b.substituteTerm?.scheme ?? "") &&
    (a.substituteTerm?.id ?? "") === (b.substituteTerm?.id ?? "")
  );
}

/** Wire shape for one decision: optional fields present only when set. */
function shapeWire(d: Decision): Decision {
  const out: Decision = { workId: d.workId, term: d.term, type: d.type, approve: d.approve };
  if (d.substituteTerm) out.substituteTerm = d.substituteTerm;
  if (d.note) out.note = d.note;
  if (d.tombstone) out.tombstone = true;
  return out;
}

/** Loads persisted decisions, tolerating missing or corrupt storage. */
function hydrate(persistKey: string, staged: Map<string, Decision>): void {
  try {
    const raw = sessionStorage.getItem(persistKey);
    if (!raw) return;
    for (const d of JSON.parse(raw) as Decision[]) {
      staged.set(decisionKey(d.workId, d.term, d.type), shapeWire(d));
    }
  } catch {
    // Corrupt or unavailable storage never blocks triage.
  }
}

/** A fresh staging store; with persistKey the staged set mirrors to
 *  sessionStorage so a reload or drill-in mid-triage loses nothing. */
export function createDecisionStore(persistKey?: string): DecisionStore {
  const staged = new Map<string, Decision>();
  if (persistKey) hydrate(persistKey, staged);
  const { subscribe, set } = writable<Decision[]>([...staged.values()]);
  const sync = (): void => {
    set([...staged.values()]);
    if (!persistKey) return;
    try {
      if (staged.size === 0) sessionStorage.removeItem(persistKey);
      else sessionStorage.setItem(persistKey, JSON.stringify([...staged.values()]));
    } catch {
      // Storage quota or availability issues never block triage.
    }
  };
  return {
    subscribe,
    stage(d: Decision): void {
      const key = decisionKey(d.workId, d.term, d.type);
      const prev = staged.get(key);
      if (prev && sameAction(prev, d)) staged.delete(key);
      else staged.set(key, shapeWire(d));
      sync();
    },
    unstage(workId: string, term: TermRef, type: SuggType): void {
      staged.delete(decisionKey(workId, term, type));
      sync();
    },
    clear(): void {
      staged.clear();
      sync();
    },
    payload(): Decision[] {
      return [...staged.values()];
    },
    reconcile(openKeys: Set<string>): Decision[] {
      const dropped: Decision[] = [];
      for (const [key, d] of staged) {
        if (!openKeys.has(key)) {
          dropped.push(d);
          staged.delete(key);
        }
      }
      if (dropped.length > 0) sync();
      return dropped;
    },
  };
}
