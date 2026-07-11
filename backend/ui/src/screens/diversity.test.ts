// The diversity-audit screen: coverage leads, category shares render against
// their named denominators, and the methodology/limits block is on the page --
// an audit number without its denominator misleads.
import { afterEach, describe, expect, it, vi } from "vitest";
import { flushSync, mount, unmount } from "svelte";
import type { DiversityReport } from "../lib/types";

const fetchDiversityAudit = vi.fn();

vi.mock("../lib/api", async () => {
  const actual = await vi.importActual<typeof import("../lib/api")>("../lib/api");
  return { ...actual, fetchDiversityAudit };
});

const Diversity = (await import("./Diversity.svelte")).default;

const REPORT: DiversityReport = {
  input: "work index (cataloging corpus: suppressed included, tombstoned excluded)",
  totalWorks: 1000,
  coveredWorks: 800,
  coverage: 0.8,
  categories: [
    { id: "lgbtqia", label: "LGBTQIA+", works: 400, shareCovered: 0.5, shareTotal: 0.4 },
    { id: "indigenous", label: "Indigenous peoples", works: 0, shareCovered: 0, shareTotal: 0 },
  ],
};

let app: Record<string, unknown> | null = null;

async function render(): Promise<void> {
  const target = document.createElement("div");
  document.body.appendChild(target);
  app = mount(Diversity, { target });
  for (let i = 0; i < 8; i++) {
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

describe("Diversity audit screen", () => {
  it("leads with coverage and renders each category against its denominators", async () => {
    fetchDiversityAudit.mockResolvedValue(REPORT);
    await render();
    const text = document.body.textContent ?? "";
    expect(text).toContain("1,000");
    expect(text).toContain("80.0%");
    const rows = [...document.querySelectorAll("table.cats tbody tr")];
    expect(rows).toHaveLength(2);
    expect(rows[0]?.textContent).toContain("LGBTQIA+");
    expect(rows[0]?.textContent).toContain("50.0%"); // of subjected
    expect(rows[0]?.textContent).toContain("40.0%"); // of collection
  });

  it("states the methodology and limits inline", async () => {
    fetchDiversityAudit.mockResolvedValue(REPORT);
    await render();
    const method = document.querySelector(".method")?.textContent ?? "";
    expect(method).toContain("nothing about creator identity");
    expect(method).toContain("editorial choice");
    expect(method).toContain("Suppressed");
  });

  it("surfaces a load failure instead of an empty report", async () => {
    fetchDiversityAudit.mockRejectedValue(new Error("boom"));
    await render();
    expect(document.querySelector(".error")).not.toBeNull();
    expect(document.querySelector("table.cats")).toBeNull();
  });

  it("renders the creator audit match-rate-first with not-stated rows", async () => {
    fetchDiversityAudit.mockResolvedValue({
      ...REPORT,
      creators: {
        totalWorks: 1000,
        matchedWorks: 30,
        matchRate: 0.03,
        resolvedCreators: 25,
        properties: [
          {
            property: "P21",
            label: "Sex or gender",
            known: 10,
            unknown: 15,
            values: [{ label: "non-binary", qid: "Q48270", creators: 6 }],
          },
        ],
      },
    });
    await render();
    const section = document.querySelector(".creators");
    expect(section?.textContent).toContain("3.0%");
    expect(section?.textContent).toContain("25");
    expect(section?.textContent).toContain("Not stated");
    expect(section?.textContent).toContain("non-binary");
    expect(section?.textContent).toContain("No person is named");
  });

  it("says the creator audit is not enabled when the block is absent", async () => {
    fetchDiversityAudit.mockResolvedValue(REPORT);
    await render();
    const section = document.querySelector(".creators");
    expect(section?.textContent).toContain("not enabled");
    expect(section?.textContent).toContain("LCATD_ENRICH_WIKIDATA");
  });
});

describe("Diversity audit scope", () => {
  it("passes the applied key=value terms to the endpoint", async () => {
    fetchDiversityAudit.mockResolvedValue(REPORT);
    await render();
    const input = document.querySelector<HTMLInputElement>("#div-scope");
    expect(input).not.toBeNull();
    input!.value = "inQll=true sources=qll";
    input!.dispatchEvent(new Event("input", { bubbles: true }));
    document.querySelector("form.scope")!.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }));
    for (let i = 0; i < 8; i++) {
      await Promise.resolve();
      flushSync();
    }
    expect(fetchDiversityAudit).toHaveBeenLastCalledWith(["inQll=true", "sources=qll"]);
  });

  it("honors an initial filter from the route query", async () => {
    fetchDiversityAudit.mockResolvedValue(REPORT);
    const target = document.createElement("div");
    document.body.appendChild(target);
    app = mount(Diversity, { target, props: { initialFilter: "inQll=true" } });
    for (let i = 0; i < 8; i++) {
      await Promise.resolve();
      flushSync();
    }
    expect(fetchDiversityAudit).toHaveBeenCalledWith(["inQll=true"]);
  });
});
