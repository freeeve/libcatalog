// RowList behavior: clamped movement, activation, focus-follows selection,
// scope-registered nav keys, and the empty state.
import { afterEach, describe, expect, it, vi } from "vitest";
import { createRawSnippet, flushSync, mount, unmount } from "svelte";
import RowList from "./RowList.svelte";
import { installKeyboard, pushScope, resetKeyboard } from "../lib/keyboard";

installKeyboard();

type ListHandle = { move: (delta: number) => void; activate: () => void };

const row = createRawSnippet((item: () => string) => ({
  render: () => `<span class="cell">${item()}</span>`,
}));

let app: Record<string, unknown> | null = null;

function mountList(props: Record<string, unknown>): { host: HTMLElement; list: ListHandle } {
  const host = document.createElement("div");
  document.body.appendChild(host);
  app = mount(RowList as never, {
    target: host,
    props: { getKey: (x: string) => x, ariaLabel: "test list", row, ...props },
  }) as Record<string, unknown>;
  flushSync();
  return { host, list: app as unknown as ListHandle };
}

afterEach(() => {
  if (app) unmount(app);
  app = null;
  resetKeyboard();
  document.body.innerHTML = "";
});

describe("RowList", () => {
  it("renders rows and marks the selected one", () => {
    const { host } = mountList({ items: ["a", "b", "c"] });
    const lis = host.querySelectorAll("li");
    expect(lis.length).toBe(3);
    expect(lis[0].classList.contains("selected")).toBe(true);
  });

  it("clamps movement at both ends", () => {
    const { host, list } = mountList({ items: ["a", "b"] });
    list.move(-1);
    flushSync();
    expect(host.querySelectorAll("li")[0].classList.contains("selected")).toBe(true);
    list.move(1);
    list.move(1);
    list.move(1);
    flushSync();
    expect(host.querySelectorAll("li")[1].classList.contains("selected")).toBe(true);
  });

  it("activate fires onactivate with the selected item", () => {
    const onactivate = vi.fn();
    const { list } = mountList({ items: ["a", "b"], onactivate });
    list.move(1);
    list.activate();
    expect(onactivate).toHaveBeenCalledWith("b", 1);
  });

  it("registers nav keys in the given scope", () => {
    const onactivate = vi.fn();
    const { host } = mountList({ items: ["a", "b"], scope: "testscope", onactivate });
    pushScope("testscope");
    document.body.dispatchEvent(new KeyboardEvent("keydown", { key: "j", bubbles: true }));
    flushSync();
    expect(host.querySelectorAll("li")[1].classList.contains("selected")).toBe(true);
    document.body.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true }));
    expect(onactivate).toHaveBeenCalledWith("b", 1);
  });

  it("shows the string empty state when there are no items", () => {
    const { host } = mountList({ items: [], empty: "nothing here" });
    expect(host.textContent).toContain("nothing here");
  });
});
