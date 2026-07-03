// Modal behavior: autofocus, Escape-to-close, Tab trap, and opener-focus
// restore on unmount.
import { afterEach, describe, expect, it, vi } from "vitest";
import { createRawSnippet, flushSync, mount, unmount } from "svelte";
import Modal from "./Modal.svelte";

const children = createRawSnippet(() => ({
  render: () => `<div><button id="m-first" data-autofocus>first</button><button id="m-last">last</button></div>`,
}));

let app: Record<string, unknown> | null = null;

function mountModal(onclose: () => void): HTMLElement {
  const host = document.createElement("div");
  document.body.appendChild(host);
  app = mount(Modal, {
    target: host,
    props: { ariaLabel: "Test dialog", onclose, children },
  }) as Record<string, unknown>;
  flushSync();
  return host;
}

afterEach(() => {
  if (app) unmount(app);
  app = null;
  document.body.innerHTML = "";
});

describe("Modal", () => {
  it("focuses the data-autofocus descendant on mount", () => {
    mountModal(() => {});
    expect(document.activeElement?.id).toBe("m-first");
  });

  it("closes on Escape and stops propagation", () => {
    const onclose = vi.fn();
    const outer = vi.fn();
    document.body.addEventListener("keydown", outer);
    const host = mountModal(onclose);
    host.querySelector("#m-first")!.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true }));
    expect(onclose).toHaveBeenCalledOnce();
    expect(outer).not.toHaveBeenCalled();
    document.body.removeEventListener("keydown", outer);
  });

  it("wraps Tab from the last focusable to the first", () => {
    const host = mountModal(() => {});
    const last = host.querySelector<HTMLElement>("#m-last")!;
    last.focus();
    last.dispatchEvent(new KeyboardEvent("keydown", { key: "Tab", bubbles: true, cancelable: true }));
    expect(document.activeElement?.id).toBe("m-first");
  });

  it("restores focus to the opener on unmount", () => {
    const opener = document.createElement("button");
    opener.id = "opener";
    document.body.appendChild(opener);
    opener.focus();
    mountModal(() => {});
    expect(document.activeElement?.id).toBe("m-first");
    unmount(app!);
    app = null;
    expect(document.activeElement?.id).toBe("opener");
  });
});
