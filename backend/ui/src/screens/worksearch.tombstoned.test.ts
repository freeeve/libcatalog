// tasks/280: work search hid nothing. On the playground that meant 49 retired
// e2e sentinels above one real book. Retired records are now excluded unless a
// "Show tombstoned" checkbox asks for them -- and the ask goes to the server, so
// the counts, the paging window and the facet rail describe the set on screen.
import { afterEach, describe, expect, it, vi } from "vitest";
import { flushSync, mount, unmount } from "svelte";
import WorkSearch from "./WorkSearch.svelte";
import { invalidateAccess, loginLocal } from "../lib/auth";
import { setConfig } from "../lib/config";
import { resetScreenStates } from "../lib/screenState.svelte";

let cleanup: (() => void) | null = null;
afterEach(() => cleanup?.());

async function tick(times = 8): Promise<void> {
  for (let i = 0; i < times; i++) {
    await Promise.resolve();
    flushSync();
  }
}

function jwtLike(): string {
  const body = btoa(JSON.stringify({ email: "staff@example.org", roles: ["librarian"] }))
    .replace(/\+/g, "-")
    .replace(/\//g, "_")
    .replace(/=+$/, "");
  return `hdr.${body}.sig`;
}

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status });
}

const live = { WorkID: "wlive000001", Title: "Gideon the Ninth", Contributors: ["Muir, Tamsyn"], Tombstoned: false };
const retired = { WorkID: "wdead000001", Title: "Retired Sentinel", Contributors: [], Tombstoned: true };

/** The works the server would return for a request, so the mock behaves like the
 *  handler under test rather than echoing whatever the screen already believed. */
function pageFor(url: string): Response {
  const mode = new URL(url, "http://x").searchParams.get("tombstoned") ?? "exclude";
  const works = mode === "only" ? [retired] : mode === "include" ? [live, retired] : [live];
  const visibility = works.map((w) => ({ value: w.Tombstoned ? "tombstoned" : "public", count: 1 }));
  return json({ works, total: works.length, matched: works.length, offset: 0, facets: { visibility } });
}

async function mountSearch(): Promise<{ host: HTMLElement; urls: string[] }> {
  setConfig({ apiBase: "", localAuth: true, provider: "test", schemes: ["lcsh"] });
  const urls: string[] = [];
  const fetchMock = vi.fn();
  vi.stubGlobal("fetch", fetchMock);
  fetchMock.mockResolvedValueOnce(json({ accessToken: jwtLike(), refreshToken: "r1", expiresIn: 900 }));
  await loginLocal("staff@example.org", "pw");
  fetchMock.mockImplementation((url: string) => {
    if (url.includes("/v1/works")) {
      urls.push(url);
      return Promise.resolve(pageFor(url));
    }
    return Promise.resolve(json({}));
  });
  const host = document.createElement("div");
  document.body.appendChild(host);
  const app = mount(WorkSearch, { target: host });
  cleanup = () => {
    unmount(app);
    vi.unstubAllGlobals();
    setConfig(null);
    invalidateAccess();
    resetScreenStates();
    localStorage.clear();
    location.hash = "";
    host.remove();
  };
  await tick(10);
  return { host, urls };
}

function showTombstoned(host: HTMLElement): HTMLInputElement {
  const box = host.querySelector<HTMLInputElement>(".show-tombstoned input");
  if (!box) throw new Error("no Show tombstoned checkbox");
  return box;
}

/** Clicks a facet-rail value by its rendered label. */
function facetValue(host: HTMLElement, label: string): HTMLInputElement {
  const row = [...host.querySelectorAll(".facet-value")].find((l) => l.textContent?.includes(label));
  const box = row?.querySelector<HTMLInputElement>("input[type=checkbox]");
  if (!box) throw new Error(`no "${label}" facet value`);
  return box;
}

function titles(host: HTMLElement): string[] {
  return [...host.querySelectorAll(".title")].map((e) => e.textContent ?? "");
}

async function toggle(box: HTMLInputElement, on: boolean): Promise<void> {
  box.checked = on;
  box.dispatchEvent(new Event("change", { bubbles: true }));
  await tick(10);
}

const modeOf = (url: string) => new URL(url, "http://x").searchParams.get("tombstoned");
const paramsOf = (url: string) => new URL(url, "http://x").searchParams;

describe("work search tombstoned filter", () => {
  it("hides retired records until asked, and starts unchecked", async () => {
    const { host, urls } = await mountSearch();

    expect(showTombstoned(host).checked).toBe(false);
    expect(modeOf(urls[0])).toBe(null); // omitted: the server's default is exclude
    expect(titles(host)).toEqual(["Gideon the Ninth"]);
  });

  it("asks the server for retired records rather than filtering the page it was given", async () => {
    const { host, urls } = await mountSearch();

    await toggle(showTombstoned(host), true);

    expect(modeOf(urls.at(-1)!)).toBe("include");
    expect(titles(host)).toEqual(["Gideon the Ninth", "Retired Sentinel"]);
  });

  it("badges a retired row so it never reads as live", async () => {
    const { host } = await mountSearch();

    await toggle(showTombstoned(host), true);

    const flags = [...host.querySelectorAll('.flag[data-kind="tombstoned"]')];
    expect(flags).toHaveLength(1);
    expect(flags[0].closest(".row-link")?.textContent).toContain("Retired Sentinel");
  });

  it("hides them again, and re-fetches", async () => {
    const { host, urls } = await mountSearch();

    await toggle(showTombstoned(host), true);
    await toggle(showTombstoned(host), false);

    expect(modeOf(urls.at(-1)!)).toBe(null);
    expect(titles(host)).toEqual(["Gideon the Ninth"]);
  });

  // The two settings can only ever match nothing together, and an empty list
  // reads as "the records are gone", not as "your filters disagree".
  it("drops a selected visibility=tombstoned facet when the box is unchecked", async () => {
    const { host, urls } = await mountSearch();

    await toggle(showTombstoned(host), true);
    await toggle(facetValue(host, "tombstoned"), true);
    expect(paramsOf(urls.at(-1)!).getAll("visibility")).toEqual(["tombstoned"]);

    await toggle(showTombstoned(host), false);

    const last = paramsOf(urls.at(-1)!);
    expect(last.get("tombstoned")).toBe(null);
    expect(last.getAll("visibility")).toEqual([]);
  });

  // Only the tombstoned value goes; an unrelated visibility filter is the
  // cataloger's, not ours to clear.
  it("keeps the cataloger's other visibility filters", async () => {
    const { host, urls } = await mountSearch();

    await toggle(showTombstoned(host), true);
    await toggle(facetValue(host, "public"), true);
    await toggle(facetValue(host, "tombstoned"), true);

    await toggle(showTombstoned(host), false);

    expect(paramsOf(urls.at(-1)!).getAll("visibility")).toEqual(["public"]);
  });
});
