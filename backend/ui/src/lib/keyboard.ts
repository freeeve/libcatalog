// One global keydown dispatcher with a scope stack. Screens push a named
// scope and register bindings into it; only the top scope plus the always-on
// "global" scope fire. Three key grammars share one registry: plain keys
// ("j", "Enter"), modifier chords ("mod+s" where mod is meta or ctrl), and
// two-step sequences ("g w"). Plain keys are ignored while focus sits in a
// form control so typing never triggers actions; mod chords fire anywhere --
// that is their point. "?" is built in: it invokes the registered help
// presenter with the currently visible bindings. keymapVersion bumps on any
// registry change so the footer legend re-renders live.
import { writable } from "svelte/store";

type Handler = (ev: KeyboardEvent) => void;

export interface Binding {
  key: string;
  description: string;
  handler: Handler;
  legend?: string;
  keyLabel?: string;
  hidden?: boolean;
  /** The scope the binding registered in ("global" = everywhere). */
  scope?: string;
}

export interface BindingSpec {
  description: string;
  handler: Handler;
  /** Short footer label; defaults to description. */
  legend?: string;
  /** Display form of the key covering its aliases too ("j/k"). */
  keyLabel?: string;
  /** Alias keys (k, ArrowUp) stay out of the footer and "?" overlay. */
  hidden?: boolean;
}

export const GLOBAL_SCOPE = "global";

/** How long a sequence prefix (the "g" of "g w") stays armed. */
const SEQUENCE_MS = 900;

/** Bumped on every push/pop/bind/unbind; the legend footer subscribes. */
export const keymapVersion = writable(0);

const scopeStack: string[] = [];
const bindings = new Map<string, Map<string, Binding>>(); // scope -> key -> binding

let pendingPrefix = "";
let pendingAt = 0;

function bump(): void {
  keymapVersion.update((n) => n + 1);
}

function scopeMap(scope: string): Map<string, Binding> {
  let m = bindings.get(scope);
  if (!m) {
    m = new Map();
    bindings.set(scope, m);
  }
  return m;
}

/** Pushes a named scope onto the stack; bindings in lower scopes go quiet. */
export function pushScope(name: string): void {
  scopeStack.push(name);
  bump();
}

/** Pops the named scope (and anything stacked above it). */
export function popScope(name: string): void {
  const i = scopeStack.lastIndexOf(name);
  if (i >= 0) scopeStack.splice(i);
  pendingPrefix = "";
  bump();
}

/** The scope currently receiving keys (global always also fires). */
export function topScope(): string {
  return scopeStack[scopeStack.length - 1] ?? GLOBAL_SCOPE;
}

/** Registers bindings in a scope; returns the unbind function. */
export function bindKeys(scope: string, map: Record<string, BindingSpec>): () => void {
  const m = scopeMap(scope);
  for (const [key, spec] of Object.entries(map)) {
    m.set(key, { key, scope, ...spec });
  }
  bump();
  return () => {
    for (const key of Object.keys(map)) m.delete(key);
    bump();
  };
}

/** Canonical chord for a keydown: "j", "Enter", "mod+s", "alt+d", "g". Meta
    and ctrl both read as "mod"; shift stays folded into the printable key. */
export function normalizeChord(ev: KeyboardEvent): string {
  let key = ev.key;
  const mod = ev.metaKey || ev.ctrlKey;
  if ((mod || ev.altKey) && key.length === 1) key = key.toLowerCase();
  let out = "";
  if (mod) out += "mod+";
  if (ev.altKey) out += "alt+";
  return out + key;
}

/** The bindings that would fire right now: top scope first, then global. */
export function activeBindings(): Binding[] {
  const out: Binding[] = [];
  const seen = new Set<string>();
  const scopes = topScope() === GLOBAL_SCOPE ? [GLOBAL_SCOPE] : [topScope(), GLOBAL_SCOPE];
  for (const scope of scopes) {
    for (const b of scopeMap(scope).values()) {
      if (!seen.has(b.key)) {
        seen.add(b.key);
        out.push(b);
      }
    }
  }
  return out;
}

/** Footer-legend view of the registry: visible single keys, plus one
    "prefix …" entry standing in for each family of visible sequences. */
export function legendBindings(): Binding[] {
  const act = activeBindings().filter((b) => !b.hidden);
  const out = act.filter((b) => !b.key.includes(" "));
  const prefixes = new Map<string, Binding>();
  for (const b of act) {
    const i = b.key.indexOf(" ");
    if (i > 0 && !prefixes.has(b.key.slice(0, i))) {
      prefixes.set(b.key.slice(0, i), b);
    }
  }
  for (const [prefix, b] of prefixes) {
    out.push({ key: `${prefix} …`, description: b.legend ?? b.description, legend: b.legend, handler: () => {} });
  }
  return out;
}

/** Display form of a binding's key: keyLabel wins, mod resolves to the
    platform's modifier. */
export function formatKey(b: Pick<Binding, "key" | "keyLabel">): string {
  const key = b.keyLabel ?? b.key;
  const mac = typeof navigator !== "undefined" && /Mac|iPhone|iPad/.test(navigator.platform);
  return key
    .replace("mod+", mac ? "⌘" : "Ctrl+")
    .replace("alt+", mac ? "⌥" : "Alt+")
    .replace("Enter", "↵")
    .replace("Escape", "esc")
    .replace("ArrowDown", "↓")
    .replace("ArrowUp", "↑");
}

type HelpPresenter = (active: Binding[]) => void;
let presentHelp: HelpPresenter | null = null;

/** Registers the "?" overlay opener (the KeyboardHelp component's mount). */
export function setHelpPresenter(fn: HelpPresenter | null): void {
  presentHelp = fn;
}

function lookup(key: string): Binding | undefined {
  return scopeMap(topScope()).get(key) ?? scopeMap(GLOBAL_SCOPE).get(key);
}

/** True when some active binding is a sequence starting with this chord. */
function armsSequence(chord: string): boolean {
  const prefix = chord + " ";
  for (const scope of topScope() === GLOBAL_SCOPE ? [GLOBAL_SCOPE] : [topScope(), GLOBAL_SCOPE]) {
    for (const key of scopeMap(scope).keys()) {
      if (key.startsWith(prefix)) return true;
    }
  }
  return false;
}

function onKeydown(ev: KeyboardEvent): void {
  if (ev.key === "Meta" || ev.key === "Control" || ev.key === "Alt" || ev.key === "Shift") return;
  const chord = normalizeChord(ev);
  const target = ev.target as HTMLElement | null;
  if (target?.closest?.("input, textarea, select, [contenteditable]") && !chord.startsWith("mod+")) {
    pendingPrefix = "";
    return;
  }
  if (pendingPrefix) {
    const fresh = Date.now() - pendingAt <= SEQUENCE_MS;
    const seq = fresh ? lookup(`${pendingPrefix} ${chord}`) : undefined;
    pendingPrefix = "";
    if (seq) {
      ev.preventDefault();
      seq.handler(ev);
      return;
    }
    // An unmatched or stale prefix falls through: the key acts normally.
  }
  if (ev.key === "?" && presentHelp) {
    ev.preventDefault();
    presentHelp(activeBindings().filter((b) => !b.hidden));
    return;
  }
  const b = lookup(chord);
  if (b) {
    ev.preventDefault();
    b.handler(ev);
    return;
  }
  if (armsSequence(chord)) {
    pendingPrefix = chord;
    pendingAt = Date.now();
    ev.preventDefault();
  }
}

export function installKeyboard(): void {
  window.addEventListener("keydown", onKeydown);
}

/** Test seam: drops every scope and binding. */
export function resetKeyboard(): void {
  scopeStack.length = 0;
  bindings.clear();
  presentHelp = null;
  pendingPrefix = "";
}
