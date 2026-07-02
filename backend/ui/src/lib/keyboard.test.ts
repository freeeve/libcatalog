import { afterEach, describe, expect, it, vi } from "vitest";
import { activeBindings, bindKeys, GLOBAL_SCOPE, installKeyboard, popScope, pushScope, resetKeyboard, setHelpPresenter } from "./keyboard";

installKeyboard();

function press(key: string, target?: HTMLElement): void {
  const ev = new KeyboardEvent("keydown", { key, bubbles: true });
  (target ?? document.body).dispatchEvent(ev);
}

afterEach(() => {
  resetKeyboard();
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
