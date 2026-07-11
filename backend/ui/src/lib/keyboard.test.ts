import { afterEach, describe, expect, it, vi } from "vitest";
import { get } from "svelte/store";
import {
  activeBindings,
  bindKeys,
  conflictingBinding,
  EDITOR_CHORDS,
  RESERVED_SHORTCUT_KEYS,
  SUGGESTED_SHORTCUT_KEY,
  shortcutKeyError,
  GLOBAL_SCOPE,
  installKeyboard,
  keymapVersion,
  legendBindings,
  normalizeChord,
  popScope,
  pushScope,
  resetKeyboard,
  resetKeymap,
  reservedReason,
  setHelpPresenter,
  setKeymapEntry,
} from "./keyboard";
import { applyPreset } from "./keymaps";

installKeyboard();

function press(key: string, target?: HTMLElement, init: KeyboardEventInit = {}): void {
  const ev = new KeyboardEvent("keydown", { key, bubbles: true, ...init });
  (target ?? document.body).dispatchEvent(ev);
}

afterEach(() => {
  resetKeyboard();
  localStorage.removeItem("lcat.keymap");
  document.body.innerHTML = "";
});

describe("keyboard scopes", () => {
  it("fires only the top scope plus global", () => {
    const below = vi.fn();
    const top = vi.fn();
    const global = vi.fn();
    bindKeys("search", { a: { description: "below", handler: below } });
    bindKeys("modal", { a: { description: "top", handler: top } });
    bindKeys(GLOBAL_SCOPE, { g: { description: "global", handler: global } });
    pushScope("search");
    pushScope("modal");
    press("a");
    press("g");
    expect(top).toHaveBeenCalledOnce();
    expect(below).not.toHaveBeenCalled();
    expect(global).toHaveBeenCalledOnce();
  });

  it("restores the scope below on pop", () => {
    const below = vi.fn();
    bindKeys("search", { a: { description: "below", handler: below } });
    pushScope("search");
    pushScope("modal");
    popScope("modal");
    press("a");
    expect(below).toHaveBeenCalledOnce();
  });

  it("ignores keys typed into form controls", () => {
    const fn = vi.fn();
    bindKeys(GLOBAL_SCOPE, { a: { description: "x", handler: fn } });
    const input = document.createElement("input");
    document.body.appendChild(input);
    press("a", input);
    expect(fn).not.toHaveBeenCalled();
  });

  it("normalizes meta and ctrl to the same mod chord", () => {
    const metaEv = new KeyboardEvent("keydown", { key: "S", metaKey: true });
    const ctrlEv = new KeyboardEvent("keydown", { key: "s", ctrlKey: true });
    const plainEv = new KeyboardEvent("keydown", { key: "?", shiftKey: true });
    expect(normalizeChord(metaEv)).toBe("mod+s");
    expect(normalizeChord(ctrlEv)).toBe("mod+s");
    expect(normalizeChord(plainEv)).toBe("?");
  });

  it("fires mod chords even while a form control has focus", () => {
    const save = vi.fn();
    const plain = vi.fn();
    bindKeys(GLOBAL_SCOPE, {
      "mod+s": { description: "save", handler: save },
      s: { description: "plain", handler: plain },
    });
    const input = document.createElement("input");
    document.body.appendChild(input);
    press("s", input, { metaKey: true });
    press("s", input);
    expect(save).toHaveBeenCalledOnce();
    expect(plain).not.toHaveBeenCalled();
  });

  it("fires a two-step sequence and swallows the prefix key", () => {
    const go = vi.fn();
    bindKeys(GLOBAL_SCOPE, { "g w": { description: "go to works", handler: go } });
    press("g");
    expect(go).not.toHaveBeenCalled();
    press("w");
    expect(go).toHaveBeenCalledOnce();
  });

  it("drops a sequence prefix after the timeout", () => {
    vi.useFakeTimers();
    const go = vi.fn();
    bindKeys(GLOBAL_SCOPE, { "g w": { description: "go to works", handler: go } });
    press("g");
    vi.advanceTimersByTime(1200);
    press("w");
    expect(go).not.toHaveBeenCalled();
    vi.useRealTimers();
  });

  it("lets an unmatched second key act normally", () => {
    const go = vi.fn();
    const other = vi.fn();
    bindKeys(GLOBAL_SCOPE, {
      "g w": { description: "go to works", handler: go },
      x: { description: "other", handler: other },
    });
    press("g");
    press("x");
    expect(go).not.toHaveBeenCalled();
    expect(other).toHaveBeenCalledOnce();
  });

  it("typing in a form control disarms a pending prefix", () => {
    const go = vi.fn();
    bindKeys(GLOBAL_SCOPE, { "g w": { description: "go to works", handler: go } });
    const input = document.createElement("input");
    document.body.appendChild(input);
    press("g");
    press("a", input);
    press("w");
    expect(go).not.toHaveBeenCalled();
  });

  it("hides alias keys from the legend and collapses sequences", () => {
    bindKeys("works", {
      j: { description: "next", legend: "j/k move", handler: () => {} },
      k: { description: "previous", hidden: true, handler: () => {} },
    });
    bindKeys(GLOBAL_SCOPE, {
      "g w": { description: "go to works", legend: "go to screen", handler: () => {} },
      "g q": { description: "go to the queue", legend: "go to screen", handler: () => {} },
    });
    pushScope("works");
    const keys = legendBindings().map((b) => b.key);
    expect(keys).toEqual(["j", "g …"]);
  });

  it("bumps keymapVersion on scope and binding changes", () => {
    const before = get(keymapVersion);
    const unbind = bindKeys("works", { j: { description: "next", handler: () => {} } });
    pushScope("works");
    popScope("works");
    unbind();
    expect(get(keymapVersion)).toBe(before + 4);
  });

  it("? presents help with the active bindings", () => {
    const shown = vi.fn();
    setHelpPresenter(shown);
    bindKeys("works", { j: { description: "next", handler: () => {} } });
    bindKeys(GLOBAL_SCOPE, { g: { description: "go", handler: () => {} } });
    pushScope("works");
    press("?");
    expect(shown).toHaveBeenCalledOnce();
    const keys = (shown.mock.calls[0][0] as { key: string }[]).map((b) => b.key);
    expect(keys).toEqual(["j", "g"]);
    expect(activeBindings().length).toBe(2);
  });
});

describe("remap layer", () => {
  it("re-keys a live binding, updates the legend, and leaves the old key inert", () => {
    const pick = vi.fn();
    bindKeys("copycat", { x: { description: "pick", legend: "pick", handler: pick } });
    pushScope("copycat");
    expect(setKeymapEntry("copycat:x", "p")).toBe(true);
    press("x");
    expect(pick).not.toHaveBeenCalled();
    press("p");
    expect(pick).toHaveBeenCalledOnce();
    expect(legendBindings().map((b) => b.key)).toEqual(["p"]);
  });

  it("persists remaps and applies them on later registration", () => {
    bindKeys("copycat", { x: { description: "pick", handler: () => {} } });
    setKeymapEntry("copycat:x", "p");
    expect(JSON.parse(localStorage.getItem("lcat.keymap") ?? "{}")).toEqual({ "copycat:x": "p" });
    // A rebind after the remap (a reload) registers straight onto "p".
    const pick = vi.fn();
    resetKeyboard();
    setKeymapEntry("copycat:x", "p");
    bindKeys("copycat", { x: { description: "pick", handler: pick } });
    pushScope("copycat");
    press("p");
    expect(pick).toHaveBeenCalledOnce();
  });

  it("refuses conflicts in the binding's scope and in global, naming the holder", () => {
    bindKeys("copycat", {
      x: { description: "pick", handler: () => {} },
      a: { description: "all", handler: () => {} },
    });
    bindKeys(GLOBAL_SCOPE, { g: { description: "go", handler: () => {} } });
    expect(conflictingBinding("copycat:x", "a")?.description).toBe("all");
    expect(conflictingBinding("copycat:x", "g")?.description).toBe("go");
    expect(conflictingBinding("copycat:x", "z")).toBeUndefined();
    expect(setKeymapEntry("copycat:x", "a")).toBe(false);
    expect(setKeymapEntry("copycat:x", "g")).toBe(false);
  });

  it("refuses reserved chords with a reason", () => {
    bindKeys("copycat", { x: { description: "pick", handler: () => {} } });
    expect(reservedReason("mod+c")).toContain("copy");
    expect(setKeymapEntry("copycat:x", "mod+c")).toBe(false);
    expect(setKeymapEntry("copycat:x", "mod+w")).toBe(false);
  });

  it("resets a single binding and the whole keymap", () => {
    const pick = vi.fn();
    bindKeys("copycat", { x: { description: "pick", handler: pick } });
    pushScope("copycat");
    setKeymapEntry("copycat:x", "p");
    expect(setKeymapEntry("copycat:x", null)).toBe(true);
    press("x");
    expect(pick).toHaveBeenCalledOnce();
    setKeymapEntry("copycat:x", "p");
    resetKeymap();
    press("x");
    expect(pick).toHaveBeenCalledTimes(2);
    expect(localStorage.getItem("lcat.keymap")).toBeNull();
  });

  it("fires allowInInputs bindings while focus sits in a form control", () => {
    const copy = vi.fn();
    const plain = vi.fn();
    bindKeys("editor", {
      "alt+c": { description: "copy field", allowInInputs: true, handler: copy },
      d: { description: "plain", handler: plain },
    });
    pushScope("editor");
    const input = document.createElement("input");
    document.body.appendChild(input);
    press("c", input, { altKey: true, code: "KeyC" });
    press("d", input);
    expect(copy).toHaveBeenCalledOnce();
    expect(plain).not.toHaveBeenCalled();
  });

  it("normalizes shifted and macOS-Option chords from the key code", () => {
    const optD = new KeyboardEvent("keydown", { key: "∂", altKey: true, code: "KeyD" });
    const ctrlShiftC = new KeyboardEvent("keydown", { key: "C", ctrlKey: true, shiftKey: true, code: "KeyC" });
    expect(normalizeChord(optD)).toBe("alt+d");
    expect(normalizeChord(ctrlShiftC)).toBe("mod+shift+c");
  });

  it("applies the Koha preset as a bundle and reports skips", () => {
    const copy = vi.fn();
    bindKeys("editor", { "alt+c": { description: "copy field", allowInInputs: true, handler: copy } });
    pushScope("editor");
    const skipped = applyPreset("koha-advanced-editor");
    expect(skipped).toEqual([]);
    press("c", undefined, { ctrlKey: true, shiftKey: true, code: "KeyC" });
    expect(copy).toHaveBeenCalledOnce();
    applyPreset("default");
    press("c", undefined, { altKey: true, code: "KeyC" });
    expect(copy).toHaveBeenCalledTimes(2);
  });

  it("keeps the legend on the remapped key with keyLabel dropped", () => {
    bindKeys("editor", {
      "alt+c": { description: "copy field", legend: "copy", keyLabel: "alt+c/x/v", handler: () => {} },
    });
    pushScope("editor");
    setKeymapEntry("editor:alt+c", "mod+shift+c");
    const b = legendBindings().find((x) => x.key === "mod+shift+c");
    expect(b).toBeDefined();
    expect(b?.keyLabel).toBeUndefined();
  });
});

// a later registrant must not evict an existing binding. A macro
// keyed "2" used to replace the MARC-tab chord in the dispatcher AND in the
// "?" overlay, then outlive the editor's own unbind.
describe("binding collisions", () => {
  afterEach(() => resetKeyboard());

  it("keeps the first binding and drops the later one", () => {
    const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
    const tab = vi.fn();
    const macro = vi.fn();
    pushScope("editor");
    bindKeys("editor", { "2": { description: "the MARC tab", handler: tab } });
    const unbindMacro = bindKeys("editor", { "2": { description: "apply macro: Stamp", handler: macro } }, "macro:");

    const held = activeBindings().filter((b) => b.key === "2");
    expect(held).toHaveLength(1);
    expect(held[0].description).toBe("the MARC tab");
    expect(warn).toHaveBeenCalled();

    // The overlay still lists the chord it never lost.
    expect(legendBindings().some((b) => b.description === "the MARC tab")).toBe(true);

    // ...and the dropped registration's unbind cannot take the survivor with it.
    unbindMacro();
    expect(activeBindings().filter((b) => b.key === "2")).toHaveLength(1);
  });

  it("still replaces a re-registration of the same action", () => {
    pushScope("editor");
    const first = vi.fn();
    const second = vi.fn();
    bindKeys("editor", { "2": { description: "the MARC tab", handler: first } });
    bindKeys("editor", { "2": { description: "the MARC tab", handler: second } });
    const held = activeBindings().filter((b) => b.key === "2");
    expect(held).toHaveLength(1);
    held[0].handler(new KeyboardEvent("keydown"));
    expect(second).toHaveBeenCalled();
    expect(first).not.toHaveBeenCalled();
  });
});

// The reserved table is mirrored in backend/batch/macros.go
// (ReservedShortcutKeys); TestReservedShortcutKeysMatchUI pins the Go side to
// these same keys. Update both together.
describe("shortcutKeyError", () => {
  it("reserves every editor chord plus the shell's two", () => {
    expect(Object.keys(RESERVED_SHORTCUT_KEYS).sort()).toEqual(["1", "2", "3", "?", "g", "m", "p"].sort());
    for (const key of Object.keys(EDITOR_CHORDS)) {
      expect(RESERVED_SHORTCUT_KEYS[key]).toBeDefined();
    }
  });

  it("refuses reserved, multi-character and duplicate keys", () => {
    expect(shortcutKeyError("2")).toMatch(/reserved for the MARC tab/);
    expect(shortcutKeyError("?")).toMatch(/reserved for the help overlay/);
    expect(shortcutKeyError("zz")).toMatch(/single character/);
    expect(shortcutKeyError("7", ["7"])).toMatch(/already used by another macro/);
  });

  it("accepts a free key, an empty key, and its own suggestion", () => {
    expect(shortcutKeyError("")).toBeUndefined();
    expect(shortcutKeyError("7", ["8"])).toBeUndefined();
    expect(shortcutKeyError(SUGGESTED_SHORTCUT_KEY)).toBeUndefined();
  });
});

// a plain-key binding must not preventDefault the key the focused
// element uses to activate itself -- Enter over a link/button, Space over a
// button/checkbox -- or that control cannot be operated from the keyboard
// (WCAG 2.1.1). The guard is target-specific: j/k/g still fire over a link.
describe("native activation keys", () => {
  afterEach(() => resetKeyboard());

  function pressCancelable(key: string, target?: HTMLElement, init: KeyboardEventInit = {}): KeyboardEvent {
    const ev = new KeyboardEvent("keydown", { key, bubbles: true, cancelable: true, ...init });
    (target ?? document.body).dispatchEvent(ev);
    return ev;
  }

  it("surrenders Enter to a focused link and does not fire the binding", () => {
    const open = vi.fn();
    bindKeys("works", { Enter: { description: "open the selected work", handler: open } });
    pushScope("works");
    const link = document.createElement("a");
    link.href = "#/batch";
    document.body.appendChild(link);
    const ev = pressCancelable("Enter", link);
    expect(ev.defaultPrevented).toBe(false);
    expect(open).not.toHaveBeenCalled();
  });

  it("surrenders Space to a focused button and does not fire the binding", () => {
    const act = vi.fn();
    bindKeys("works", { " ": { description: "select all", handler: act } });
    pushScope("works");
    const button = document.createElement("button");
    document.body.appendChild(button);
    const ev = pressCancelable(" ", button);
    expect(ev.defaultPrevented).toBe(false);
    expect(act).not.toHaveBeenCalled();
  });

  it("still fires a bound Enter when focus is not on an activatable element", () => {
    const open = vi.fn();
    bindKeys("works", { Enter: { description: "open the selected work", handler: open } });
    pushScope("works");
    const ev = pressCancelable("Enter", document.body);
    expect(ev.defaultPrevented).toBe(true);
    expect(open).toHaveBeenCalledOnce();
  });

  it("does not surrender a non-activation key over a link -- j/k still fire", () => {
    const next = vi.fn();
    bindKeys("works", { j: { description: "next row", handler: next } });
    pushScope("works");
    const link = document.createElement("a");
    link.href = "#/batch";
    document.body.appendChild(link);
    const ev = pressCancelable("j", link);
    expect(ev.defaultPrevented).toBe(true);
    expect(next).toHaveBeenCalledOnce();
  });

  it("keeps mod+Enter firing even over a link (mod chords are exempt)", () => {
    const run = vi.fn();
    bindKeys("works", { "mod+Enter": { description: "run", handler: run } });
    pushScope("works");
    const link = document.createElement("a");
    link.href = "#/batch";
    document.body.appendChild(link);
    const ev = pressCancelable("Enter", link, { metaKey: true });
    expect(ev.defaultPrevented).toBe(true);
    expect(run).toHaveBeenCalledOnce();
  });
});
