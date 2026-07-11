// an absolute feed/CDN cover URL must render as-is. CoverPanel used
// to prepend apiBase() unconditionally, turning the OverDrive CDN URL into a
// same-origin path the server answers with the SPA HTML shell -- a broken image
// for every work whose cover is a feed passthrough.
import { afterEach, describe, expect, it, vi } from "vitest";
import { flushSync, mount, unmount } from "svelte";

vi.mock("../lib/config", async () => {
  const actual = await vi.importActual<typeof import("../lib/config")>("../lib/config");
  return { ...actual, apiBase: () => "", isReadOnly: () => true };
});

const CoverPanel = (await import("./CoverPanel.svelte")).default;

const CDN_COVER =
  "https://img2.od-cdn.com/ImageType-400/1933-1/%7BCCD5580D%7DIMG400.JPG";

let app: Record<string, unknown> | null = null;

function render(cover: string): void {
  const target = document.createElement("div");
  document.body.appendChild(target);
  app = mount(CoverPanel, { target, props: { workId: "w1", cover } });
}

function imgSrc(): string | null {
  return document.querySelector<HTMLImageElement>("img.cover")?.getAttribute("src") ?? null;
}

afterEach(() => {
  if (app) unmount(app);
  app = null;
  document.body.innerHTML = "";
  vi.clearAllMocks();
});

describe("CoverPanel cover src", () => {
  it("uses an absolute feed/CDN URL verbatim, no apiBase prefix or cache-buster", () => {
    render(CDN_COVER);
    flushSync();
    expect(imgSrc()).toBe(CDN_COVER);
  });

  it("prefixes apiBase and the cache-buster on a site-relative editorial blob path", () => {
    render("covers/w1.jpg");
    flushSync();
    expect(imgSrc()).toBe("/covers/w1.jpg?v=0");
  });

  it("renders no image when the work has no cover", () => {
    render("");
    flushSync();
    expect(imgSrc()).toBeNull();
    expect(document.querySelector(".muted")?.textContent).toBe("none");
  });
});
