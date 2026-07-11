// the item panel saved a whole-list replacement built from a list it
// had loaded minutes earlier, sending no If-Match. The second of two catalogers
// deleted the first one's copy, and the panel said "saved 2 items". A barcode
// names one physical book on one shelf.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { flushSync, mount, unmount } from "svelte";

const fetchItems = vi.fn();
const putItems = vi.fn();

vi.mock("../lib/api", async () => {
  const actual = await vi.importActual<typeof import("../lib/api")>("../lib/api");
  return {
    ...actual,
    fetchItems,
    putItems,
    fetchItemTemplates: vi.fn(async () => ({ templates: [] })),
  };
});

const { ConflictError } = await import("../lib/api");
const ItemsPanel = (await import("./ItemsPanel.svelte")).default;

const WORK = "wabc123def456";
const INST = "iabc123def456";

let app: Record<string, unknown> | null = null;

function itemsResponse(etag: string, barcodes: string[]) {
  return { etag, items: { [INST]: barcodes.map((b) => ({ barcode: b, callNumber: "", location: "", note: "" })) } };
}

/** The conflict path awaits putItems, then load(), then renders. */
async function settle(): Promise<void> {
  for (let i = 0; i < 8; i++) await Promise.resolve();
  flushSync();
}

/** The success notice, which a refused save must never show. */
function okNotice(host: HTMLElement): string {
  return host.querySelector(".ok")?.textContent ?? "";
}

/** Svelte sets an input's value property, not its attribute, so innerHTML is
 *  no place to look for the rows the panel is showing. */
function barcodes(host: HTMLElement): string[] {
  return [...host.querySelectorAll('input[aria-label="Barcode"]')].map((i) => (i as HTMLInputElement).value);
}

async function mountPanel(): Promise<HTMLElement> {
  const host = document.createElement("div");
  document.body.appendChild(host);
  app = mount(ItemsPanel, { target: host, props: { workId: WORK, instanceId: INST } }) as Record<string, unknown>;
  flushSync();
  await settle();
  return host;
}

function button(host: HTMLElement, label: string): HTMLButtonElement {
  const b = [...host.querySelectorAll("button")].find((x) => (x.textContent ?? "").trim() === label);
  if (!b) throw new Error(`no ${label} button among ${[...host.querySelectorAll("button")].map((x) => x.textContent)}`);
  return b as HTMLButtonElement;
}

/** Save is gated on `dirty`, so a save is only reachable after a human edits a
 * row -- which is exactly the read-modify-write cycle is about. */
function editFirstBarcode(host: HTMLElement, value: string): void {
  const input = host.querySelector('input[aria-label="Barcode"]') as HTMLInputElement;
  input.value = value;
  input.dispatchEvent(new Event("input", { bubbles: true }));
  flushSync();
}

beforeEach(() => {
  fetchItems.mockReset();
  putItems.mockReset();
  fetchItems.mockResolvedValue(itemsResponse("etag-1", ["zzlu-seed-1"]));
  putItems.mockResolvedValue({ etag: "etag-2" });
});

afterEach(() => {
  if (app) unmount(app);
  app = null;
  document.body.innerHTML = "";
});

describe("ItemsPanel concurrent save", () => {
  it("sends the etag it loaded the list under", async () => {
    const host = await mountPanel();
    editFirstBarcode(host, "zzlu-B");
    button(host, "Save items").click();
    flushSync();
    await settle();

    expect(putItems).toHaveBeenCalledTimes(1);
    expect(putItems.mock.calls[0][3]).toBe("etag-1");
  });

  it("never saves without a token", async () => {
    const host = await mountPanel();
    editFirstBarcode(host, "zzlu-B");
    button(host, "Save items").click();
    flushSync();
    await settle();

    for (const call of putItems.mock.calls) {
      expect(call[3], `putItems called with ifMatch=${JSON.stringify(call[3])}`).toBeTruthy();
    }
  });

  it("reloads and warns instead of reporting success when another cataloger got there first", async () => {
    const host = await mountPanel();
    putItems.mockRejectedValueOnce(new ConflictError({ workId: WORK, etag: "etag-2", nquads: "" }));
    fetchItems.mockResolvedValue(itemsResponse("etag-2", ["zzlu-seed-1", "zzlu-A"]));

    editFirstBarcode(host, "zzlu-B");
    button(host, "Save items").click();
    flushSync();
    await settle();

    expect(host.textContent).toContain("another cataloger changed this record's items");
    // The panel used to say "saved 2 items" here, which was the whole harm.
    expect(okNotice(host)).toBe("");
    // The reload must show their copy, so the cataloger can see what they nearly
    // destroyed and re-apply on top of it.
    expect(barcodes(host)).toContain("zzlu-A");
  });

  it("saves again under the fresh token after a conflict", async () => {
    const host = await mountPanel();
    putItems.mockRejectedValueOnce(new ConflictError({ workId: WORK, etag: "etag-2", nquads: "" }));
    fetchItems.mockResolvedValue(itemsResponse("etag-2", ["zzlu-seed-1", "zzlu-A"]));

    editFirstBarcode(host, "zzlu-B");
    button(host, "Save items").click();
    flushSync();
    await settle();

    editFirstBarcode(host, "zzlu-B");
    button(host, "Save items").click();
    flushSync();
    await settle();

    expect(putItems).toHaveBeenCalledTimes(2);
    expect(putItems.mock.calls[1][3]).toBe("etag-2");
  });

  it("still reports an ordinary failure as a failure", async () => {
    const host = await mountPanel();
    const { ApiError } = await import("../lib/api");
    putItems.mockRejectedValueOnce(new ApiError(500, "grain write failed"));

    editFirstBarcode(host, "zzlu-B");
    button(host, "Save items").click();
    flushSync();
    await settle();

    expect(host.textContent).toContain("grain write failed");
    expect(host.textContent).not.toContain("another cataloger");
  });
});
