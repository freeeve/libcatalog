// One global keydown dispatcher with a scope stack. Screens push a named
// scope and register bindings into it; only the top scope plus the always-on
// "global" scope fire. Three key grammars share one registry: plain keys
// ("j", "Enter"), modifier chords ("mod+s" where mod is meta or ctrl), and
// two-step sequences ("g w"). Plain keys are ignored while focus sits in a
// form control so typing never triggers actions (bindings can opt in with
// allowInInputs); mod chords fire anywhere -- that is their point. "?" is
// built in: it invokes the registered help presenter with the currently
// visible bindings. keymapVersion bumps on any registry change so the footer
// legend re-renders live. A user keymap (tasks/075) re-keys bindings at
// registration under stable action ids ("scope:default-key") and persists in
// localStorage; remaps propagate to the legend and overlay because both read
// the registry.
import { writable } from "svelte/store";

type Handler = (ev: KeyboardEvent) => void;

export interface Binding {
  key: string;
  description: string;
  handler: Handler;
  legend?: string;
  keyLabel?: string;
  hidden?: boolean;
  legendHidden?: boolean;
  /** The scope the binding registered in ("global" = everywhere). */
  scope?: string;
  /** Stable remap identity: "scope:default-key". */
  id?: string;
  /** The key the call site registered, before any remap. */
  defaultKey?: string;
  /** Fires even while focus sits in a form control. */
  allowInInputs?: boolean;
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
  /** Stays out of the footer legend but keeps its "?" overlay row (large
      op families that would flood the rail). */
  legendHidden?: boolean;
  /** Fire even while focus sits in a form control (editor field ops). */
  allowInInputs?: boolean;
}

export const GLOBAL_SCOPE = "global";

/** How long a sequence prefix (the "g" of "g w") stays armed. */
const SEQUENCE_MS = 900;

/** Where the user keymap persists across reloads. */
const KEYMAP_STORAGE = "lcat.keymap";

/** Chords a remap may never claim, with the reason shown on refusal. */
const RESERVED: Record<string, string> = {
  "mod+c": "system copy",
  "mod+x": "system cut",
  "mod+v": "system paste",
  "mod+a": "select all",
  "mod+z": "undo",
  "mod+w": "closes the browser tab",
  "mod+q": "quits the browser",
  "mod+t": "opens a browser tab",
  "mod+n": "opens a browser window",
  "?": "opens this help overlay",
};

/** The single-character chords the work editor claims, and what each does.
    WorkEditor binds its handlers onto exactly these keys, so the editor and
    the macro screen cannot disagree about which are spoken for (tasks/237).
    "mod+s" is not here: it is not one character, so no macro shortcut can
    reach it. */
export const EDITOR_CHORDS: Record<string, string> = {
  "1": "the Native tab",
  "2": "the MARC tab",
  "3": "the History tab",
  p: "preview staged changes",
  m: "the live MARC preview pane",
};

/** Every single character a macro shortcut may not claim: the editor's
    chords, plus the two the shell owns everywhere. Mirrored in
    backend/batch/macros.go (ReservedShortcutKeys); keyboard.test.ts and
    TestReservedShortcutKeysMatchUI pin the two tables together. */
export const RESERVED_SHORTCUT_KEYS: Record<string, string> = {
  ...EDITOR_CHORDS,
  "?": "the help overlay",
  g: "the go-to-screen prefix",
};

/** A key a macro may safely take -- the placeholder and the suggestion the
    macro screen offers. */
export const SUGGESTED_SHORTCUT_KEY = "4";

/** Why `key` cannot be a macro shortcut, or undefined when it is free.
    `macros` are the other macros' keys, which may not be doubled up either:
    two macros on one key means one of them can never fire, and which one is
    an accident of registration order. */
export function shortcutKeyError(key: string, macros: string[] = []): string | undefined {
  if (!key) return undefined;
  if ([...key].length !== 1) return "a shortcut key must be a single character";
  if (RESERVED_SHORTCUT_KEYS[key]) return `"${key}" is reserved for ${RESERVED_SHORTCUT_KEYS[key]}`;
  if (macros.includes(key)) return `"${key}" is already used by another macro`;
  return undefined;
}

/** Bumped on every push/pop/bind/unbind; the legend footer subscribes. */
export const keymapVersion = writable(0);

const scopeStack: string[] = [];
const bindings = new Map<string, Map<string, Binding>>(); // scope -> key -> binding

/** action id -> remapped key, loaded once and written through. */
let keymap: Record<string, string> = loadKeymap();

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

function loadKeymap(): Record<string, string> {
  try {
    const raw = localStorage.getItem(KEYMAP_STORAGE);
    const parsed: unknown = raw ? JSON.parse(raw) : {};
    if (parsed && typeof parsed === "object" && !Array.isArray(parsed)) {
      return Object.fromEntries(Object.entries(parsed).filter(([, v]) => typeof v === "string")) as Record<
        string,
        string
      >;
    }
  } catch {
    // A corrupt keymap falls back to defaults rather than wedging startup.
  }
  return {};
}

function saveKeymap(): void {
  try {
    if (Object.keys(keymap).length === 0) localStorage.removeItem(KEYMAP_STORAGE);
    else localStorage.setItem(KEYMAP_STORAGE, JSON.stringify(keymap));
  } catch {
    // Storage full/denied: the remap still applies for this session.
  }
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

/** Registers bindings in a scope; returns the unbind function. Each binding
    gets the stable action id "scope:<idPrefix><default-key>"; a user keymap
    entry for that id re-keys it here, so remaps propagate everywhere with no
    call-site changes. A remapped binding drops its keyLabel (it described the
    default key's aliases).

    `idPrefix` namespaces registrants that share a scope but are not the same
    action -- macro shortcuts live in the editor scope, but "editor:macro:2"
    is not "editor:2".

    A key already held by a *different* action is left alone: the first
    registration wins, the later one is dropped with a warning, and its
    unbind is a no-op for that key. Overwriting silently is what let a macro
    keyed "2" delete the MARC-tab binding -- from the dispatcher and from the
    "?" overlay, which renders from this same registry -- and then leak, since
    the evicted owner's unbind deletes by identity and no longer matched
    (tasks/237). Re-registering the same id (a remount) still replaces. */
export function bindKeys(scope: string, map: Record<string, BindingSpec>, idPrefix = ""): () => void {
  const m = scopeMap(scope);
  const registered: Binding[] = [];
  for (const [defaultKey, spec] of Object.entries(map)) {
    const id = `${scope}:${idPrefix}${defaultKey}`;
    const key = keymap[id] ?? defaultKey;
    const held = m.get(key);
    if (held && held.id !== id) {
      console.warn(`keyboard: "${key}" is already bound to ${held.description} in scope "${scope}"; ignoring ${spec.description}`);
      continue;
    }
    const b: Binding = { ...spec, key, scope, id, defaultKey };
    if (key !== defaultKey) b.keyLabel = undefined;
    m.set(key, b);
    registered.push(b);
  }
  bump();
  return () => {
    // Delete by the binding's current key: a live remap may have re-keyed it
    // since registration.
    for (const b of registered) {
      if (m.get(b.key) === b) m.delete(b.key);
    }
    bump();
  };
}

/** Canonical chord for a keydown: "j", "Enter", "mod+s", "alt+d", "g". Meta
    and ctrl both read as "mod"; shift stays folded into the printable key
    except alongside another modifier ("mod+shift+c"), where the base key
    comes from the layout-independent code so macOS Option glyphs ("∂") and
    shifted characters cannot hide the letter. */
export function normalizeChord(ev: KeyboardEvent): string {
  let key = ev.key;
  const mod = ev.metaKey || ev.ctrlKey;
  if (mod || ev.altKey) {
    const fromCode = /^Key([A-Z])$/.exec(ev.code)?.[1] ?? /^Digit([0-9])$/.exec(ev.code)?.[1];
    if (fromCode) key = fromCode.toLowerCase();
    else if (key.length === 1) key = key.toLowerCase();
  }
  let out = "";
  if (mod) out += "mod+";
  if (ev.altKey) out += "alt+";
  if (ev.shiftKey && (mod || ev.altKey) && key.length === 1) out += "shift+";
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
  const act = activeBindings().filter((b) => !b.hidden && !b.legendHidden);
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
    .replace("shift+", mac ? "⇧" : "Shift+")
    .replace("Enter", "↵")
    .replace("Escape", "esc")
    .replace("ArrowDown", "↓")
    .replace("ArrowUp", "↑");
}

/** Why a chord can never be remapped onto, or undefined when it is fair
    game. Covers the system clipboard, browser-critical chords, and "?". */
export function reservedReason(key: string): string | undefined {
  return RESERVED[key];
}

/** The already-registered binding a remap of `id` onto `key` would collide
    with: same scope or global, different action. */
export function conflictingBinding(id: string, key: string): Binding | undefined {
  const scope = id.slice(0, id.indexOf(":"));
  for (const s of scope === GLOBAL_SCOPE ? [GLOBAL_SCOPE] : [scope, GLOBAL_SCOPE]) {
    const hit = scopeMap(s).get(key);
    if (hit && hit.id !== id) return hit;
  }
  return undefined;
}

/** Remaps an action onto a new key (null restores the default), re-keying a
    live registration in place and persisting the keymap. Returns false when
    the target key is reserved or already taken in the action's scope
    (restoring a default whose key another action claimed also refuses). */
export function setKeymapEntry(id: string, key: string | null): boolean {
  if (key !== null && (reservedReason(key) || conflictingBinding(id, key))) return false;
  const m = scopeMap(id.slice(0, id.indexOf(":")));
  const live = [...m.values()].find((b) => b.id === id);
  if (live) {
    const next = key ?? live.defaultKey ?? live.key;
    if (conflictingBinding(id, next)) return false;
    m.delete(live.key);
    live.key = next;
    if (key !== null) live.keyLabel = undefined;
    m.set(live.key, live);
  }
  if (key === null) delete keymap[id];
  else keymap[id] = key;
  saveKeymap();
  bump();
  return true;
}

/** The current user keymap (action id -> key), for the redefine UI. */
export function keymapEntries(): Record<string, string> {
  return { ...keymap };
}

/** Drops every remap, restoring all defaults (live registrations re-key). */
export function resetKeymap(): void {
  for (const id of Object.keys(keymap)) setKeymapEntry(id, null);
}

/** Applies a preset keymap as one bundle: each entry remaps individually so
    a reserved or conflicting chord is skipped, not fatal; entries outside
    the preset (a user's own remaps) stay. Returns the skipped action ids. */
export function applyKeymap(entries: Record<string, string>): string[] {
  const skipped: string[] = [];
  for (const [id, key] of Object.entries(entries)) {
    if (!setKeymapEntry(id, key)) skipped.push(id);
  }
  return skipped;
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
    // Editor field ops opt in to firing over form controls (tasks/075).
    const b = lookup(chord);
    if (b?.allowInInputs) {
      ev.preventDefault();
      b.handler(ev);
    }
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

/** Test seam: drops every scope, binding, and in-memory remap. */
export function resetKeyboard(): void {
  scopeStack.length = 0;
  bindings.clear();
  presentHelp = null;
  pendingPrefix = "";
  keymap = {};
}
