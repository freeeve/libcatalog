// One global keydown dispatcher with a scope stack. Screens push a named
// scope and register bindings into it; only the top scope plus the always-on
// "global" scope fire. Keys are ignored while focus sits in a form control so
// typing never triggers actions. "?" is built in: it invokes the registered
// help presenter with the currently active bindings.
type Handler = (ev: KeyboardEvent) => void;

export interface Binding {
  key: string;
  description: string;
  handler: Handler;
}

export interface BindingSpec {
  description: string;
  handler: Handler;
}

export const GLOBAL_SCOPE = "global";

const scopeStack: string[] = [];
const bindings = new Map<string, Map<string, Binding>>(); // scope -> key -> binding

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
}

/** Pops the named scope (and anything stacked above it). */
export function popScope(name: string): void {
  const i = scopeStack.lastIndexOf(name);
  if (i >= 0) scopeStack.splice(i);
}

/** The scope currently receiving keys (global always also fires). */
export function topScope(): string {
  return scopeStack[scopeStack.length - 1] ?? GLOBAL_SCOPE;
}

/** Registers bindings in a scope; returns the unbind function. */
export function bindKeys(scope: string, map: Record<string, BindingSpec>): () => void {
  const m = scopeMap(scope);
  for (const [key, spec] of Object.entries(map)) {
    m.set(key, { key, description: spec.description, handler: spec.handler });
  }
  return () => {
    for (const key of Object.keys(map)) m.delete(key);
  };
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

type HelpPresenter = (active: Binding[]) => void;
let presentHelp: HelpPresenter | null = null;

/** Registers the "?" overlay opener (the KeyboardHelp component's mount). */
export function setHelpPresenter(fn: HelpPresenter | null): void {
  presentHelp = fn;
}

function lookup(key: string): Binding | undefined {
  return scopeMap(topScope()).get(key) ?? scopeMap(GLOBAL_SCOPE).get(key);
}

function onKeydown(ev: KeyboardEvent): void {
  const target = ev.target as HTMLElement | null;
  if (target?.closest?.("input, textarea, select, [contenteditable]")) return;
  if (ev.metaKey || ev.ctrlKey || ev.altKey) return;
  if (ev.key === "?" && presentHelp) {
    ev.preventDefault();
    presentHelp(activeBindings());
    return;
  }
  const b = lookup(ev.key);
  if (b) {
    ev.preventDefault();
    b.handler(ev);
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
}
