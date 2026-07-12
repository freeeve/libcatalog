// The crosswalk panel: equivalents of the work's subjects, grouped by the
// source heading, strength-labeled, already-present terms excluded, unknown
// terms display-only, and Add staging the same subjects op the lookup uses.
import { afterEach, describe, expect, it, vi } from "vitest";
import { flushSync, mount, unmount } from "svelte";
import type { FieldValue, Op } from "../lib/types";

const fetchTermEquivalents = vi.fn();
const resolveTermURIs = vi.fn();

vi.mock("../lib/api", async () => {
  const actual = await vi.importActual<typeof import("../lib/api")>("../lib/api");
  return { ...actual, fetchTermEquivalents, resolveTermURIs };
});
vi.mock("../lib/config", async () => {
  const actual = await vi.importActual<typeof import("../lib/config")>("../lib/config");
  return { ...actual, isReadOnly: () => false };
});

const EquivalentsPanel = (await import("./EquivalentsPanel.svelte")).default;

const FAST = "http://id.worldcat.org/fast/995592";
const HOMO = "https://homosaurus.org/v5/homoit0009999";
const GND = "https://d-nb.info/gnd/123";

function fv(v: string): FieldValue {
  return { v, iri: true, prov: "feed", node: "n1" };
}

let app: Record<string, unknown> | null = null;

async function render(subjects: FieldValue[], ops: Op[], onadd: (uri: string) => void): Promise<void> {
  const target = document.createElement("div");
  document.body.appendChild(target);
  app = mount(EquivalentsPanel, { target, props: { subjects, ops, onadd } });
  for (let i = 0; i < 10; i++) {
    await Promise.resolve();
    flushSync();
  }
}

afterEach(() => {
  if (app) unmount(app);
  app = null;
  document.body.innerHTML = "";
  vi.clearAllMocks();
});

describe("EquivalentsPanel", () => {
  it("suggests grouped, strength-labeled equivalents and stages adds", async () => {
    fetchTermEquivalents.mockResolvedValue({
      equivalents: [
        { id: HOMO, scheme: "homosaurus", labels: { en: "Sapphic poets" }, strength: "pivot-close", known: true },
        { id: GND, strength: "exact", known: false },
      ],
    });
    resolveTermURIs.mockResolvedValue({ terms: { [FAST]: { scheme: "fast", id: FAST, labels: { en: "Lesbian poets" } } } });
    const added: string[] = [];
    await render([fv(FAST)], [], (uri) => added.push(uri));

    const panel = document.querySelector(".equivalents");
    expect(panel?.querySelector("summary")?.textContent).toContain("2");
    expect(panel?.textContent).toContain("≈ Lesbian poets"); // source heading resolved
    expect(panel?.textContent).toContain("Sapphic poets");
    expect(panel?.querySelector(".s-pivot-close")).not.toBeNull();

    // Unknown terms note their state but stage the same URI-only op --
    // the record carries the URI; the label resolves if that vocabulary
    // is loaded later (task 429).
    expect(panel?.textContent).toContain("not in a loaded vocabulary");
    expect(panel?.querySelectorAll("button.add")).toHaveLength(2);
    const uriAdd = panel?.querySelector<HTMLButtonElement>("button.add--uri");
    expect(uriAdd?.textContent).toContain("Add URI");

    panel?.querySelector<HTMLButtonElement>("button.add:not(.add--uri)")?.click();
    expect(added).toEqual([HOMO]);
    uriAdd?.click();
    expect(added).toEqual([HOMO, GND]);
  });

  it("excludes terms the record already has -- stored or staged", async () => {
    fetchTermEquivalents.mockResolvedValue({
      equivalents: [{ id: HOMO, labels: { en: "Sapphic poets" }, strength: "exact", known: true }],
    });
    resolveTermURIs.mockResolvedValue({ terms: {} });
    const stagedAdd: Op = { resource: "work", path: "subjects", action: "add", value: { v: HOMO, iri: true } };
    await render([fv(FAST)], [stagedAdd], () => {});
    // The only suggestion is already staged: the panel renders nothing.
    expect(document.querySelector(".equivalents")).toBeNull();
  });

  it("renders nothing for a record with no controlled subjects", async () => {
    await render([], [], () => {});
    expect(document.querySelector(".equivalents")).toBeNull();
    expect(fetchTermEquivalents).not.toHaveBeenCalled();
  });
});
