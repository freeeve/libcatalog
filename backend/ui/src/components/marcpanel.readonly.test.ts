// MarcPanel rendered an ungated "Save MARC" on read-only instances.
// Clicking it posted the execute path, which the server refused -- and, before
// the backend fix, refused with a 500. SaveBar had already decided what this
// should look like; the MARC panel bypassed it.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { flushSync, mount, unmount } from "svelte";
import { setConfig } from "../lib/config";

/** The config shape, taken from the seam rather than re-declared. */
type Config = NonNullable<Parameters<typeof setConfig>[0]>;

const postMarc = vi.fn();

vi.mock("../lib/api", async () => {
  const actual = await vi.importActual<typeof import("../lib/api")>("../lib/api");
  return {
    ...actual,
    fetchMarc: vi.fn(async () => ({
      etag: "etag-1",
      records: [
        {
          leader: "00000nam a2200000 a 4500",
          fields: [{ tag: "245", ind1: "1", ind2: "0", subfields: [{ code: "a", value: "A Book" }] }],
        },
      ],
      knownLoss: {},
    })),
    postMarc,
  };
});

const MarcPanel = (await import("./MarcPanel.svelte")).default;

let app: Record<string, unknown> | null = null;

function mountPanel(cfg: Partial<Config>): HTMLElement {
  setConfig({ apiBase: "", ...cfg } as Config);
  const host = document.createElement("div");
  document.body.appendChild(host);
  app = mount(MarcPanel, { target: host, props: { workId: "wabc123def456" } }) as Record<string, unknown>;
  flushSync();
  return host;
}

/** settle lets MarcPanel's onMount load() resolve before assertions. */
async function settle(): Promise<void> {
  await Promise.resolve();
  await Promise.resolve();
  flushSync();
}

function buttonLabels(host: HTMLElement): string[] {
  return [...host.querySelectorAll("button")].map((b) => (b.textContent ?? "").trim());
}

beforeEach(() => {
  postMarc.mockReset();
  postMarc.mockResolvedValue({ etag: "etag-2", diff: { added: ["+q"], removed: [] } });
});

afterEach(() => {
  if (app) unmount(app);
  app = null;
  setConfig(null);
  document.body.innerHTML = "";
});

describe("MarcPanel write gating", () => {
  it("offers Save MARC on a writable instance", async () => {
    const host = mountPanel({});
    await settle();
    expect(buttonLabels(host)).toContain("Save MARC");
  });

  it("hides Save MARC in the read-only demo, and keeps Preview delta", async () => {
    const host = mountPanel({ readOnly: true });
    await settle();
    const labels = buttonLabels(host);
    expect(labels).not.toContain("Save MARC");
    expect(labels).toContain("Preview delta");
    expect(host.textContent).toContain("read-only demo");
  });

  it("never posts the execute path in the read-only demo", async () => {
    const host = mountPanel({ readOnly: true });
    await settle();
    for (const b of host.querySelectorAll("button")) (b as HTMLButtonElement).click();
    flushSync();
    await settle();
    for (const call of postMarc.mock.calls) {
      expect(call[3]?.dryRun, `postMarc called with ${JSON.stringify(call[3])}`).toBe(true);
    }
  });

  it("offers a demo save in the sandbox that dry-runs instead of writing", async () => {
    const host = mountPanel({ readOnly: true, sandbox: true });
    await settle();
    const labels = buttonLabels(host);
    expect(labels).toContain("Save MARC (demo)");
    expect(labels).not.toContain("Save MARC");

    const save = [...host.querySelectorAll("button")].find((b) => b.textContent?.includes("Save MARC (demo)"));
    (save as HTMLButtonElement).click();
    flushSync();
    await settle();

    expect(postMarc).toHaveBeenCalledTimes(1);
    const opts = postMarc.mock.calls[0][3];
    expect(opts.dryRun).toBe(true);
    expect(opts.ifMatch).toBeUndefined();
  });
});
