// tasks/228: a mounted FixedFieldGrid survives a tag change (in-place tag
// edits, keyed lists shifting), so its slot table must follow the tag --
// a stale table mislabels positions and writes runs at wrong byte offsets.
import { afterEach, describe, expect, it } from "vitest";
import { flushSync, mount, unmount } from "svelte";
import FixedFieldGrid from "./FixedFieldGrid.svelte";
import { resetScreenStates, screenState } from "../lib/screenState.svelte";

let cleanup: (() => void) | null = null;
afterEach(() => cleanup?.());

function labels(host: HTMLElement): string[] {
  return [...host.querySelectorAll(".slot-label")].map((el) => el.textContent?.trim() ?? "");
}

describe("FixedFieldGrid", () => {
  it("re-derives the slot table when the tag prop changes", () => {
    const host = document.createElement("div");
    document.body.appendChild(host);
    // screenState hands back a real $state proxy (rune syntax is
    // unavailable in a .test.ts), so mutating props.tag is reactive.
    const props = screenState("ffg-test", () => ({ tag: "LDR", value: "", onchange: () => {} }));
    const instance = mount(FixedFieldGrid, { target: host, props });
    cleanup = () => {
      unmount(instance);
      host.remove();
      resetScreenStates();
    };
    flushSync();
    expect(labels(host).some((l) => l.startsWith("Record status"))).toBe(true);
    expect(labels(host).some((l) => l.startsWith("Language"))).toBe(false);

    props.tag = "008";
    flushSync();
    // The 008 table replaces the leader's: its Language slot appears and
    // the leader's Record status is gone.
    expect(labels(host).some((l) => l.startsWith("Language"))).toBe(true);
    expect(labels(host).some((l) => l.startsWith("Record status"))).toBe(false);
  });
});
