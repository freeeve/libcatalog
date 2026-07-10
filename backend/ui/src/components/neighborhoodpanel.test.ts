// tasks/251: the details pane and the neighborhood walk must describe the same
// term. The panel now owns the whole identity block, so after a walk the URI
// shown above the buttons is the URI the "Use this term" button stages.
import { afterEach, describe, expect, it, vi } from "vitest";
import { flushSync, mount, unmount } from "svelte";
import type { Term } from "../lib/types";

const resolveTerm = vi.fn();

vi.mock("../lib/api", async () => {
  const actual = await vi.importActual<typeof import("../lib/api")>("../lib/api");
  return { ...actual, resolveTerm };
});

const NeighborhoodPanel = (await import("./NeighborhoodPanel.svelte")).default;

const CATS: Term = { scheme: "lcsh", id: "http://id.loc.gov/authorities/subjects/sh85021262", labels: { en: "Cats" }, narrower: ["http://id.loc.gov/authorities/subjects/sh85072368"] };
const KITTENS: Term = { scheme: "lcsh", id: "http://id.loc.gov/authorities/subjects/sh85072368", labels: { en: "Kittens" }, definition: { en: "Young cats." } };

let app: Record<string, unknown> | null = null;

function render(onselect: (t: Term) => void) {
  const target = document.createElement("div");
  document.body.appendChild(target);
  app = mount(NeighborhoodPanel, { target, props: { term: CATS, onselect } });
  return target;
}

afterEach(() => {
  if (app) unmount(app);
  app = null;
  document.body.innerHTML = "";
  vi.clearAllMocks();
});

describe("NeighborhoodPanel identity", () => {
  it("names the walked term before the walk", () => {
    resolveTerm.mockResolvedValue(KITTENS);
    render(() => {});
    flushSync();
    expect(document.querySelector(".ident")?.textContent).toContain("Cats");
    expect(document.querySelector(".ident-uri")?.textContent).toBe(CATS.id);
  });

  it("moves the identity URI to the walked term, matching what Use-this-term stages", async () => {
    resolveTerm.mockResolvedValue(KITTENS);
    const onselect = vi.fn();
    const target = render(onselect);

    // Wait for the narrower link to resolve, then walk to it.
    const link = await vi.waitFor(() => {
      flushSync();
      const b = [...target.querySelectorAll<HTMLButtonElement>("button.linkish")].find((el) => el.textContent?.includes("Kittens"));
      if (!b) throw new Error("narrower link not resolved yet");
      return b;
    });
    link.click();
    flushSync();

    // The identity block now describes Kittens, not Cats.
    const shownURI = target.querySelector(".ident-uri")?.textContent;
    expect(target.querySelector(".ident")?.textContent).toContain("Kittens");
    expect(shownURI).toBe(KITTENS.id);

    // And "Use this term" stages exactly that term -- the URI above and the id
    // staged must be the same term (the whole point of tasks/251).
    const use = [...target.querySelectorAll<HTMLButtonElement>("button.use")].find((el) => el.textContent?.includes("Use this term"))!;
    use.click();
    expect(onselect).toHaveBeenCalledOnce();
    expect(onselect.mock.calls[0][0].id).toBe(shownURI);
  });
});
