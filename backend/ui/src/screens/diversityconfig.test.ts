// The crosswalk-configuration screen: the persisted override loads into the
// editor, the term histogram renders with counts, checked terms land on the
// target category (URIs as exact matches, headings/tags as keywords), preview
// runs without persisting, save PUTs the categories, and remove DELETEs.
import { afterEach, describe, expect, it, vi } from "vitest";
import { flushSync, mount, unmount } from "svelte";
import type { AuditTermsPage, CrosswalkView, DiversityReport } from "../lib/types";

const fetchDiversityCrosswalk = vi.fn();
const fetchAuditTerms = vi.fn();
const saveDiversityCrosswalk = vi.fn();
const deleteDiversityCrosswalk = vi.fn();
const previewDiversityCrosswalk = vi.fn();

vi.mock("../lib/api", async () => {
  const actual = await vi.importActual<typeof import("../lib/api")>("../lib/api");
  return {
    ...actual,
    fetchDiversityCrosswalk,
    fetchAuditTerms,
    saveDiversityCrosswalk,
    deleteDiversityCrosswalk,
    previewDiversityCrosswalk,
  };
});

const DiversityConfig = (await import("./DiversityConfig.svelte")).default;

const SEED: CrosswalkView["seed"] = [
  { id: "lgbtqia", label: "LGBTQIA+", keywords: ["lesbian", "gay"], schemes: ["homosaurus"] },
];

const VIEW: CrosswalkView = { seed: SEED, effective: SEED };

const TERMS: AuditTermsPage = {
  totalWorks: 100,
  uris: [
    { uri: "https://homosaurus.org/v4/homoit0001378", label: "Transgender women", scheme: "homosaurus", works: 82 },
    { uri: "https://homosaurus.org/v4/homoit0001379", label: "Transgender men", scheme: "homosaurus", works: 76 },
  ],
  uriTotal: 2,
  headings: [{ label: "Drag queens", works: 12 }],
  headingTotal: 1,
  tags: [{ label: "trans joy", works: 4 }],
  tagTotal: 1,
};

let app: Record<string, unknown> | null = null;

async function tick(times = 8): Promise<void> {
  for (let i = 0; i < times; i++) {
    await Promise.resolve();
    flushSync();
  }
}

async function render(view: CrosswalkView = VIEW, terms: AuditTermsPage = TERMS): Promise<void> {
  fetchDiversityCrosswalk.mockResolvedValue(view);
  fetchAuditTerms.mockResolvedValue(terms);
  const target = document.createElement("div");
  document.body.appendChild(target);
  app = mount(DiversityConfig, { target, props: {} });
  await tick();
}

afterEach(() => {
  if (app) unmount(app);
  app = null;
  document.body.innerHTML = "";
  vi.clearAllMocks();
});

function click(sel: string): void {
  const el = [...document.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent?.includes(sel));
  if (!el) throw new Error(`no button matching ${sel}`);
  el.click();
}

describe("DiversityConfig", () => {
  it("renders the histogram with counts and the seed reference", async () => {
    await render();
    const main = document.querySelector("main#main.divconfig");
    expect(main?.querySelector("h1")?.textContent).toContain("Diversity crosswalk");
    const terms = document.querySelector(".terms");
    expect(terms?.textContent).toContain("Transgender women");
    expect(terms?.textContent).toContain("82");
    expect(document.querySelector(".seedlist")?.textContent).toContain("LGBTQIA+");
    expect(document.body.textContent).toContain("lenses, not a partition");
  });

  it("loads a stored override into the editor and surfaces a broken one", async () => {
    await render({
      ...VIEW,
      override: [{ id: "veterans", label: "Veterans", keywords: ["veterans"] }],
      toml: '[[category]]\nid = "veterans"\n',
    });
    const cat = document.querySelector(".cat");
    expect(cat?.querySelector<HTMLInputElement>("input.id")?.value).toBe("veterans");
    expect(cat?.querySelector<HTMLInputElement>("input.label")?.value).toBe("Veterans");

    if (app) unmount(app);
    document.body.innerHTML = "";
    vi.clearAllMocks();
    await render({ ...VIEW, toml: "[[category]\n", broken: "parse crosswalk: bare keys" });
    expect(document.querySelector(".error")?.textContent).toContain("no longer parses");
  });

  it("sends checked terms to the target category: URIs exact, labels as keywords", async () => {
    await render();
    click("Add category");
    await tick();
    const cat = document.querySelector(".cat")!;
    cat.querySelector<HTMLInputElement>("input.id")!.value = "lgbtqia-trans";
    cat.querySelector<HTMLInputElement>("input.id")!.dispatchEvent(new Event("input", { bubbles: true }));

    const boxes = [...document.querySelectorAll<HTMLInputElement>('.terms input[type="checkbox"]')];
    expect(boxes.length).toBe(4);
    for (const b of boxes.slice(0, 2)) {
      b.click();
    }
    // The heading term too: it must become a keyword, not a URI.
    boxes[2].click();
    await tick();
    click("Add 3 selected");
    await tick();

    const uris = document.querySelector<HTMLTextAreaElement>(".cat textarea")!.value;
    expect(uris).toContain("homoit0001378");
    expect(uris).toContain("homoit0001379");
    const keywords = [...document.querySelectorAll<HTMLInputElement>(".cat input")].map((i) => i.value).join("|");
    expect(keywords).toContain("Drag queens");
  });

  it("previews without persisting and saves the drafted categories", async () => {
    const previewReport: DiversityReport = {
      input: "work index",
      totalWorks: 100,
      coveredWorks: 90,
      coverage: 0.9,
      categories: [{ id: "lgbtqia-trans", label: "Transgender people", works: 82, shareCovered: 0.9, shareTotal: 0.82 }],
    };
    previewDiversityCrosswalk.mockResolvedValue(previewReport);
    saveDiversityCrosswalk.mockResolvedValue({
      ...VIEW,
      override: [{ id: "lgbtqia-trans", label: "Transgender people" }],
      toml: "x",
    });
    await render();
    click("Add category");
    await tick();
    const id = document.querySelector<HTMLInputElement>(".cat input.id")!;
    id.value = "lgbtqia-trans";
    id.dispatchEvent(new Event("input", { bubbles: true }));
    const label = document.querySelector<HTMLInputElement>(".cat input.label")!;
    label.value = "Transgender people";
    label.dispatchEvent(new Event("input", { bubbles: true }));
    await tick();

    click("Preview counts");
    await tick();
    expect(previewDiversityCrosswalk).toHaveBeenCalledWith(
      { categories: [{ id: "lgbtqia-trans", label: "Transgender people", keywords: undefined, uris: undefined, schemes: undefined }] },
      [],
    );
    expect(saveDiversityCrosswalk).not.toHaveBeenCalled();
    expect(document.querySelector(".preview")?.textContent).toContain("82");
    expect(document.querySelector(".preview caption")?.textContent).toContain("nothing saved");

    click("Save override");
    await tick();
    expect(saveDiversityCrosswalk).toHaveBeenCalledTimes(1);
    expect(document.querySelector(".notice")?.textContent).toContain("Saved");
  });

  it("extends a seed category and removes the override", async () => {
    deleteDiversityCrosswalk.mockResolvedValue(undefined);
    await render();
    click("Extend");
    await tick();
    expect(document.querySelector<HTMLInputElement>(".cat input.id")?.value).toBe("lgbtqia");

    click("Remove override");
    await tick();
    expect(deleteDiversityCrosswalk).toHaveBeenCalledTimes(1);
    expect(document.querySelector(".notice")?.textContent).toContain("built-in seed");
  });

  it("saves a pasted TOML document through the advanced editor", async () => {
    saveDiversityCrosswalk.mockResolvedValue(VIEW);
    await render();
    const ta = document.querySelector<HTMLTextAreaElement>(".advanced textarea")!;
    ta.value = '[[category]]\nid = "zines"\n';
    ta.dispatchEvent(new Event("input", { bubbles: true }));
    await tick();
    click("Save TOML");
    await tick();
    expect(saveDiversityCrosswalk).toHaveBeenCalledWith({ toml: '[[category]]\nid = "zines"\n' });
  });
});
