// The diversity-audit screen: coverage leads (gauge + stats), category shares
// render against their named denominators with inline bars, the exclusive
// composition strip stacks honestly, snapshots record and unlock trends, and
// the methodology/limits block is on the page.
import { afterEach, describe, expect, it, vi } from "vitest";
import { flushSync, mount, unmount } from "svelte";
import type { DiversityReport, DiversitySnapshot } from "../lib/types";

const fetchDiversityAudit = vi.fn();
const fetchDiversitySnapshots = vi.fn();
const recordDiversitySnapshot = vi.fn();

vi.mock("../lib/api", async () => {
  const actual = await vi.importActual<typeof import("../lib/api")>("../lib/api");
  return { ...actual, fetchDiversityAudit, fetchDiversitySnapshots, recordDiversitySnapshot };
});

const Diversity = (await import("./Diversity.svelte")).default;

const REPORT: DiversityReport = {
  input: "work index (cataloging corpus: suppressed included, tombstoned excluded)",
  totalWorks: 1000,
  coveredWorks: 800,
  coverage: 0.8,
  multiplicity: { uncategorized: 200, matchedOne: 450, matchedMulti: 150 },
  categories: [
    { id: "lgbtqia", label: "LGBTQIA+", works: 400, shareCovered: 0.5, shareTotal: 0.4 },
    { id: "indigenous", label: "Indigenous peoples", works: 0, shareCovered: 0, shareTotal: 0 },
  ],
};

function snap(date: string, over?: Partial<DiversityReport>): DiversitySnapshot {
  return { ...REPORT, ...over, date };
}

let app: Record<string, unknown> | null = null;

async function tick(times = 8): Promise<void> {
  for (let i = 0; i < times; i++) {
    await Promise.resolve();
    flushSync();
  }
}

async function render(props: Record<string, unknown> = {}): Promise<void> {
  const target = document.createElement("div");
  document.body.appendChild(target);
  app = mount(Diversity, { target, props });
  await tick();
}

afterEach(() => {
  if (app) unmount(app);
  app = null;
  document.body.innerHTML = "";
  vi.clearAllMocks();
});

function arm(snapshots: DiversitySnapshot[] = []): void {
  fetchDiversityAudit.mockResolvedValue(REPORT);
  fetchDiversitySnapshots.mockResolvedValue({ snapshots });
}

describe("Diversity audit screen", () => {
  it("leads with the coverage gauge and renders share bars per category", async () => {
    arm();
    await render();
    const gauge = document.querySelector(".gauge");
    expect(gauge?.getAttribute("aria-label")).toContain("80.0%");
    const rows = [...document.querySelectorAll("table.cats tbody tr")];
    expect(rows).toHaveLength(2);
    expect(rows[0]?.textContent).toContain("LGBTQIA+");
    expect(rows[0]?.textContent).toContain("50.0%");
    const bar = rows[0]?.querySelector<HTMLElement>(".bar");
    expect(parseFloat(bar!.style.width)).toBeCloseTo(50);
    const tick0 = rows[0]?.querySelector<HTMLElement>(".tick");
    expect(parseFloat(tick0!.style.left)).toBeCloseTo(40);
  });

  it("stacks the exclusive composition strip to 100% of the collection", async () => {
    arm();
    await render();
    const segs = [...document.querySelectorAll<HTMLElement>(".strip .seg")];
    const total = segs.reduce((sum, s) => sum + parseFloat(s.style.width), 0);
    expect(Math.round(total)).toBe(100);
    expect(document.querySelector(".legend")?.textContent).toContain("matches 2+ categories");
  });

  it("shows the record button and the accumulate hint when no snapshots exist", async () => {
    arm([]);
    await render();
    const trends = document.querySelector(".trends");
    expect(trends?.querySelector("button")?.textContent).toContain("Record snapshot");
    expect(trends?.textContent).toContain("No snapshots yet");
    expect(document.querySelector(".area")).toBeNull();
  });

  it("records a snapshot and refreshes the series", async () => {
    arm([]);
    recordDiversitySnapshot.mockResolvedValue(snap("2026-07-11"));
    await render();
    fetchDiversitySnapshots.mockResolvedValue({ snapshots: [snap("2026-07-11")] });
    document.querySelector<HTMLButtonElement>(".trends button")!.click();
    await tick();
    expect(recordDiversitySnapshot).toHaveBeenCalledWith([]);
    expect(document.querySelector(".trends")?.textContent).toContain("a second unlocks");
  });

  it("draws the stacked composition area and per-category sparklines from two snapshots", async () => {
    arm([
      snap("2026-06-01", { multiplicity: { uncategorized: 300, matchedOne: 400, matchedMulti: 50 } }),
      snap("2026-07-11"),
    ]);
    await render();
    const area = document.querySelector(".area");
    expect(area).not.toBeNull();
    expect(area?.querySelectorAll("path")).toHaveLength(4);
    const panels = [...document.querySelectorAll(".multiples .panel")];
    expect(panels).toHaveLength(2);
    expect(panels[0]?.textContent).toContain("LGBTQIA+");
    const pts = panels[0]?.querySelector(".spark")?.getAttribute("points");
    expect(pts?.split(" ")).toHaveLength(2);
    expect(document.querySelector(".axis")?.textContent).toContain("2026-06-01");
  });

  it("renders operator benchmarks as neutral markers with named sources", async () => {
    fetchDiversityAudit.mockResolvedValue({
      ...REPORT,
      categories: [
        { id: "lgbtqia", label: "LGBTQIA+", works: 400, shareCovered: 0.5, shareTotal: 0.4, benchmark: 0.41, benchmarkSource: "CCBC 2025" },
        { id: "indigenous", label: "Indigenous peoples", works: 0, shareCovered: 0, shareTotal: 0 },
      ],
    });
    fetchDiversitySnapshots.mockResolvedValue({ snapshots: [] });
    await render();
    const rows = [...document.querySelectorAll("table.cats tbody tr")];
    const bench = rows[0]?.querySelector<HTMLElement>(".bench");
    expect(parseFloat(bench!.style.left)).toBeCloseTo(41);
    expect(bench?.getAttribute("title")).toContain("CCBC 2025");
    expect(rows[0]?.textContent).toContain("41.0%");
    expect(rows[0]?.textContent).toContain("CCBC 2025");
    // A category without a benchmark renders no marker, and the note says
    // deltas are interpretation, not scores.
    expect(rows[1]?.querySelector(".bench")).toBeNull();
    expect(document.querySelector(".bench-note")?.textContent).toContain("never as a score");
  });

  it("omits the benchmark column when no category carries one", async () => {
    arm();
    await render();
    expect(document.querySelector(".bench")).toBeNull();
    expect(document.querySelector(".bench-note")).toBeNull();
    const headers = [...document.querySelectorAll("table.cats thead th")].map((h) => h.textContent);
    expect(headers.join("|")).not.toContain("Benchmark");
  });

  it("passes the applied key=value terms to both endpoints", async () => {
    arm();
    await render();
    const input = document.querySelector<HTMLInputElement>("#div-scope");
    input!.value = "inQll=true";
    input!.dispatchEvent(new Event("input", { bubbles: true }));
    document.querySelector("form.scope")!.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }));
    await tick();
    expect(fetchDiversityAudit).toHaveBeenLastCalledWith(["inQll=true"]);
    expect(fetchDiversitySnapshots).toHaveBeenLastCalledWith(["inQll=true"]);
  });

  it("honors an initial filter from the route query", async () => {
    arm();
    await render({ initialFilter: "inQll=true" });
    expect(fetchDiversityAudit).toHaveBeenCalledWith(["inQll=true"]);
  });

  it("renders the creator audit match-rate-first with not-stated rows and bars", async () => {
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
    fetchDiversitySnapshots.mockResolvedValue({ snapshots: [] });
    await render();
    const section = document.querySelector(".creators");
    expect(section?.textContent).toContain("3.0%");
    expect(section?.textContent).toContain("Not stated");
    expect(section?.textContent).toContain("non-binary");
    expect(section?.textContent).toContain("No person is named");
    expect(section?.querySelectorAll(".bar").length).toBeGreaterThan(0);
  });

  it("says the creator audit is not enabled when the block is absent", async () => {
    arm();
    await render();
    const section = document.querySelector(".creators");
    expect(section?.textContent).toContain("not enabled");
    expect(section?.textContent).toContain("LCATD_ENRICH_WIKIDATA");
  });

  it("states the methodology inline and surfaces load failures", async () => {
    arm();
    await render();
    expect(document.querySelector(".method")?.textContent).toContain("nothing about creator identity");
    if (app) unmount(app);
    app = null;
    document.body.innerHTML = "";
    vi.clearAllMocks();
    fetchDiversityAudit.mockRejectedValue(new Error("boom"));
    fetchDiversitySnapshots.mockResolvedValue({ snapshots: [] });
    await render();
    expect(document.querySelector(".error")).not.toBeNull();
    expect(document.querySelector("table.cats")).toBeNull();
  });
});
