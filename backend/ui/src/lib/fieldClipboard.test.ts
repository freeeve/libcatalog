// the clipboard round-trips through the module $state -- reads
// re-proxy stored entries and pushes may receive $state proxies, and
// structuredClone cannot clone either without a snapshot first.
import { beforeEach, describe, expect, it } from "vitest";
import { clipAt, clipClear, clipPeek, clipPush, fieldClipboard } from "./fieldClipboard.svelte";
import { screenState } from "./screenState.svelte";
import type { MarcField } from "./types";

const field = (tag: string): MarcField => ({
  tag,
  ind1: " ",
  ind2: " ",
  subfields: [{ code: "a", value: "Value for " + tag }],
});

describe("fieldClipboard", () => {
  beforeEach(() => clipClear());

  it("push then peek returns a usable plain copy", () => {
    clipPush(field("008"));
    const back = clipPeek();
    const sub = back?.subfields?.[0];
    if (!back || !sub) throw new Error("peek returned nothing usable");
    expect(back.tag).toBe("008");
    expect(sub.value).toBe("Value for 008");
    // The copy is detached: mutating it never reaches the stored entry.
    sub.value = "mutated";
    expect(clipPeek()?.subfields?.[0]?.value).toBe("Value for 008");
  });

  it("clipAt picks any entry; newest first", () => {
    clipPush(field("100"));
    clipPush(field("245"));
    expect(clipAt(0)?.tag).toBe("245");
    expect(clipAt(1)?.tag).toBe("100");
    expect(clipAt(2)).toBeUndefined();
  });

  it("accepts a $state proxy on push", () => {
    // screenState hands back a real $state deep proxy -- the same shape a
    // grid row passes to alt+c (rune syntax is unavailable in a .test.ts).
    const proxied = screenState("clip-proxy-test", () => ({ f: field("336") }));
    clipPush(proxied.f);
    expect(clipPeek()?.tag).toBe("336");
    expect(fieldClipboard.entries.length).toBe(1);
  });
});
