// The enrichment screen: sources load into the run form, kicking queues a
// job and the board shows it with live counters, polling stops once every
// job is terminal, a DONE queue-mode job links to reviewing its
// suggestions, and a FAILED job shows its classified error.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { flushSync, mount, unmount } from "svelte";
import type { EnrichJob } from "../lib/types";

const fetchEnrichSources = vi.fn();
const fetchEnrichJobs = vi.fn();
const createEnrichJob = vi.fn();
const runEnrichSource = vi.fn();

vi.mock("../lib/api", async () => {
  const actual = await vi.importActual<typeof import("../lib/api")>("../lib/api");
  return { ...actual, fetchEnrichSources, fetchEnrichJobs, createEnrichJob, runEnrichSource };
});

const Enrichment = (await import("./Enrichment.svelte")).default;

function job(over: Partial<EnrichJob>): EnrichJob {
  return {
    id: "j1",
    source: "sru-subjects",
    requester: "eve@example.org",
    status: "RUNNING",
    createdAt: "2026-07-11T12:00:00Z",
    ...over,
  };
}

let app: Record<string, unknown> | null = null;

async function tick(times = 8): Promise<void> {
  for (let i = 0; i < times; i++) {
    await Promise.resolve();
    flushSync();
  }
}

async function render(): Promise<void> {
  const target = document.createElement("div");
  document.body.appendChild(target);
  app = mount(Enrichment, { target, props: {} });
  await tick();
}

beforeEach(() => {
  vi.useFakeTimers();
});

afterEach(() => {
  if (app) unmount(app);
  app = null;
  document.body.innerHTML = "";
  vi.clearAllMocks();
  vi.useRealTimers();
});

describe("Enrichment screen", () => {
  it("lists sources and shows a running job's live counters with the activity bar", async () => {
    fetchEnrichSources.mockResolvedValue({ sources: ["wikidata", "sru-subjects"] });
    fetchEnrichJobs.mockResolvedValue({
      jobs: [
        job({
          filters: [["inQll", "true"]],
          stats: { batches: 233, skippedBatches: 5, elapsedMs: 269495 },
        }),
      ],
    });
    await render();
    const opts = [...document.querySelectorAll("#enr-source option")].map((o) => o.textContent);
    expect(opts).toEqual(["sru-subjects", "wikidata"]); // sorted
    const card = document.querySelector(".job");
    expect(card?.textContent).toContain("sru-subjects");
    expect(card?.textContent).toContain("inQll=true");
    expect(card?.textContent).toContain("233 batches");
    expect(card?.textContent).toContain("5 skipped");
    expect(card?.textContent).toContain("4m29s");
    expect(card?.querySelector(".bar")).not.toBeNull();
  });

  it("renders a determinate bar, percent, started time and ETA when the source sized its run (task 439)", async () => {
    fetchEnrichSources.mockResolvedValue({ sources: ["bibliocommons"] });
    fetchEnrichJobs.mockResolvedValue({
      jobs: [
        job({
          source: "bibliocommons",
          startedAt: "2026-07-11T12:00:08Z",
          // Halfway through 4286 driver terms after 45 minutes.
          stats: { batches: 2143, total: 4286, elapsedMs: 2700000 },
        }),
      ],
    });
    await render();
    const card = document.querySelector(".job")!;
    expect(card.textContent).toContain("50% · 2143/4286");
    expect(card.textContent).toContain("queued");
    expect(card.textContent).toContain("started");
    expect(card.textContent).toContain("~45m left");
    const fill = card.querySelector<HTMLElement>(".bar .fill");
    expect(fill).not.toBeNull();
    expect(fill!.style.width).toBe("50%");
    expect(card.querySelector(".bar .pulse")).toBeNull();
  });

  it("keeps the indeterminate pulse for a lazily-sized source (no total)", async () => {
    fetchEnrichSources.mockResolvedValue({ sources: ["wikidata"] });
    fetchEnrichJobs.mockResolvedValue({
      jobs: [job({ stats: { batches: 12, elapsedMs: 30000 } })],
    });
    await render();
    const card = document.querySelector(".job")!;
    expect(card.querySelector(".bar .pulse")).not.toBeNull();
    expect(card.querySelector(".bar .fill")).toBeNull();
    expect(card.textContent).toContain("12 batches");
    expect(card.textContent).not.toContain("%");
  });

  it("polls while a job runs and stops once the board is terminal", async () => {
    fetchEnrichSources.mockResolvedValue({ sources: ["wikidata"] });
    fetchEnrichJobs.mockResolvedValueOnce({ jobs: [job({})] });
    await render();
    expect(fetchEnrichJobs).toHaveBeenCalledTimes(1);

    fetchEnrichJobs.mockResolvedValue({
      jobs: [job({ status: "DONE", result: { source: "sru-subjects", mode: "queue", works: 41 } })],
    });
    await vi.advanceTimersByTimeAsync(3000);
    await tick();
    expect(fetchEnrichJobs).toHaveBeenCalledTimes(2);
    // Terminal board: the DONE job links to review, the bar is gone, and no
    // further poll is scheduled.
    const card = document.querySelector(".job");
    expect(card?.querySelector(".bar")).toBeNull();
    expect(card?.textContent).toContain("41 works");
    expect(card?.textContent).toContain("with suggestions queued");
    expect(card?.querySelector('a[href="#/queue?provenance=PIPELINE"]')?.textContent).toContain("Review suggestions");
    await vi.advanceTimersByTimeAsync(10000);
    expect(fetchEnrichJobs).toHaveBeenCalledTimes(2);
  });

  it("kicks a job with the scoped filters and refreshes the board", async () => {
    fetchEnrichSources.mockResolvedValue({ sources: ["sru-subjects"] });
    fetchEnrichJobs.mockResolvedValue({ jobs: [] });
    createEnrichJob.mockResolvedValue(job({ status: "QUEUED" }));
    await render();
    const scope = document.querySelector<HTMLInputElement>("#enr-scope")!;
    scope.value = "inQll=true";
    scope.dispatchEvent(new Event("input", { bubbles: true }));
    await tick();
    fetchEnrichJobs.mockResolvedValue({ jobs: [job({ status: "QUEUED" })] });
    document.querySelector("form")!.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }));
    await tick();
    expect(createEnrichJob).toHaveBeenCalledWith("sru-subjects", ["inQll=true"], []);
    expect(document.querySelector(".job")?.textContent).toContain("QUEUED");
    expect(document.querySelector(".job")?.textContent).toContain("waiting for the worker");
  });

  it("shows a failed job's classified error and a sync run's inline result", async () => {
    fetchEnrichSources.mockResolvedValue({ sources: ["crosswalk-homosaurus"] });
    fetchEnrichJobs.mockResolvedValue({
      jobs: [job({ status: "FAILED", error: "enrichment upstream failed" })],
    });
    runEnrichSource.mockResolvedValue({ source: "crosswalk-homosaurus", mode: "queue", works: 3 });
    await render();
    expect(document.querySelector(".job .error")?.textContent).toContain("enrichment upstream failed");

    const buttons = [...document.querySelectorAll<HTMLButtonElement>("button")];
    buttons.find((b) => b.textContent?.includes("Run now"))!.click();
    await tick();
    expect(runEnrichSource).toHaveBeenCalledWith("crosswalk-homosaurus", []);
    expect(document.body.textContent).toContain("3 works");
    expect(document.body.textContent).toContain("with suggestions queued");
  });
});
