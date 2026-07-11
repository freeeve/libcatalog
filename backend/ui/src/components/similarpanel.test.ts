// the "more like this" panel. The rail is computed, not catalogued, so
// every row has to explain itself -- and the explanation arrives as opaque values:
// subjects are authority IRIs, tags and contributors and series are already human
// text. A panel that renders "shares https://homosaurus.org/v3/homoit0000669" has
// answered nobody's question.
import { afterEach, describe, expect, it, vi } from "vitest";
import { flushSync, mount, unmount } from "svelte";

const fetchSimilar = vi.fn();
const resolveTermURIs = vi.fn();

vi.mock("../lib/api", async () => {
  const actual = await vi.importActual<typeof import("../lib/api")>("../lib/api");
  return { ...actual, fetchSimilar, resolveTermURIs };
});

const SimilarPanel = (await import("./SimilarPanel.svelte")).default;

const WORK = "wabc123def456";
let app: Record<string, unknown> | null = null;

function render() {
  const target = document.createElement("div");
  document.body.appendChild(target);
  app = mount(SimilarPanel, { target, props: { workId: WORK } });
  return target;
}

async function settle(): Promise<void> {
  await vi.waitFor(() => {
    if (document.body.textContent?.includes("Scoring")) throw new Error("still loading");
  });
  flushSync();
}

afterEach(() => {
  if (app) unmount(app);
  app = null;
  document.body.innerHTML = "";
  vi.clearAllMocks();
});

describe("SimilarPanel", () => {
  it("resolves a shared subject IRI to its label and leaves free text alone", async () => {
    fetchSimilar.mockResolvedValue({
      similar: [
        {
          workId: "wneighbour01",
          title: "The House of the Spirits",
          score: 1.2,
          shared: ["https://ex.org/homoit0000669", "Lobel, Arnold."],
        },
      ],
    });
    resolveTermURIs.mockResolvedValue({
      terms: { "https://ex.org/homoit0000669": { id: "https://ex.org/homoit0000669", scheme: "homosaurus", labels: { en: "Transgender people" } } },
    });

    const target = render();
    await settle();

    const text = target.textContent ?? "";
    expect(text).toContain("The House of the Spirits");
    expect(text).toContain("Transgender people");
    // The contributor name is already human; it must not be mangled or dropped.
    expect(text).toContain("Lobel, Arnold.");
    // And the raw IRI must never reach the reader.
    expect(text).not.toContain("https://ex.org/homoit0000669");
    // Only the IRI was sent for resolution.
    expect(resolveTermURIs).toHaveBeenCalledWith(["https://ex.org/homoit0000669"]);
  });

  it("renders one element per shared term, so a comma inside a label is not a separator", async () => {
    //. "Lobel, Arnold." is one contributor and "Lesbians' writings,
    // Canadian" is one subject heading; a comma-joined line names four things.
    fetchSimilar.mockResolvedValue({
      similar: [
        {
          workId: "wneighbour01",
          title: "A Book",
          score: 1.2,
          shared: ["https://ex.org/fast996602", "Lobel, Arnold."],
        },
      ],
    });
    resolveTermURIs.mockResolvedValue({
      terms: {
        "https://ex.org/fast996602": {
          id: "https://ex.org/fast996602",
          scheme: "fast",
          labels: { en: "Lesbians' writings, Canadian" },
        },
      },
    });

    const target = render();
    await settle();

    const terms = [...target.querySelectorAll(".why .term")].map((el) => el.textContent);
    expect(terms).toEqual(["Lesbians' writings, Canadian", "Lobel, Arnold."]);
  });

  it("falls back to the URI when the vocabulary cannot resolve it", async () => {
    fetchSimilar.mockResolvedValue({
      similar: [{ workId: "wn", title: "A Book", score: 1, shared: ["https://ex.org/unknown"] }],
    });
    // an unresolvable URI is absent from the map, not null.
    resolveTermURIs.mockResolvedValue({ terms: {} });

    const target = render();
    await settle();

    expect(target.textContent).toContain("https://ex.org/unknown");
    expect(target.textContent).not.toContain("undefined");
  });

  it("says what to do about a work with no neighbours instead of showing nothing", async () => {
    fetchSimilar.mockResolvedValue({ similar: [] });

    const target = render();
    await settle();

    expect(target.textContent).toContain("No neighbours");
    expect(target.textContent).toMatch(/controlled subject/);
    expect(resolveTermURIs).not.toHaveBeenCalled();
  });

  it("links each neighbour to its own editor", async () => {
    fetchSimilar.mockResolvedValue({ similar: [{ workId: "wneighbour01", title: "Herculine", score: 1 }] });

    const target = render();
    await settle();

    const a = target.querySelector("a");
    expect(a?.getAttribute("href")).toBe("#/works/wneighbour01");
  });

  it("reports a failed scoring call rather than rendering an empty rail", async () => {
    fetchSimilar.mockRejectedValue(new Error("boom"));

    const target = render();
    await settle();

    const alert = target.querySelector('[role="alert"]');
    expect(alert).not.toBeNull();
    expect(target.textContent).not.toContain("No neighbours");
  });

  it("labels the rail as computed, so a surprising suggestion is not read as cataloguing", async () => {
    fetchSimilar.mockResolvedValue({ similar: [{ workId: "wn", title: "A Book", score: 1 }] });

    const target = render();
    await settle();

    expect(target.textContent).toMatch(/Suggested automatically/);
  });
});
