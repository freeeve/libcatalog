// Renders the instance links field and pins the enriched link display:
// a grain-carried 856 $3 annotation wins (libcodex v0.15.0),
// OverDrive URL shapes get heuristic labels otherwise, image links get an
// inline thumbnail, and unrecognized links keep the compact host › tail
// form.
import { describe, expect, it, vi } from "vitest";
import { flushSync, mount, unmount } from "svelte";
import ProfileForm from "./ProfileForm.svelte";
import type { ResourceDoc } from "../lib/types";

const instance: ResourceDoc = {
  id: "i-001",
  fields: {
    links: [
      { v: "http://link.overdrive.com/?websiteID=173&titleID=798942", iri: true, prov: "feed:marc", node: "_:l1" },
      {
        v: "https://img1.od-cdn.com/ImageType-100/1095-1/%7B8BF51CB6%7DImg100.jpg",
        iri: true,
        prov: "feed:marc",
        node: "_:l2",
      },
      { v: "https://example.org/about", iri: true, prov: "editorial:", node: "_:l3" },
      { v: "https://samples.overdrive.com/?x=1", iri: true, prov: "feed:marc", node: "_:l4", annotation: "Excerpt" },
    ],
  },
};

describe("ProfileForm links rendering", () => {
  it("labels known link shapes and renders image links as thumbnails", () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response("{}")));
    const host = document.createElement("div");
    document.body.appendChild(host);
    const app = mount(ProfileForm, {
      target: host,
      props: { res: instance, resource: "i-001", kind: "instance", ops: [], onstage: () => {}, onunstage: () => {} },
    });
    flushSync();

    const labels = [...host.querySelectorAll(".linklabel")].map((el) => el.textContent);
    expect(labels).toContain("OverDrive title page");
    expect(labels).toContain("Cover image");
    // The grain-carried $3 annotation beats the URL heuristic ("Sample (excerpt)").
    expect(labels).toContain("Excerpt");
    expect(labels).not.toContain("Sample (excerpt)");

    const thumb = host.querySelector("img.linkthumb") as HTMLImageElement;
    expect(thumb).toBeTruthy();
    expect(thumb.src).toContain("od-cdn.com");
    expect(thumb.alt).toBe("Cover image");

    const plain = [...host.querySelectorAll("a.linkval")].find((a) => (a as HTMLAnchorElement).href.includes("example.org"));
    expect(plain?.querySelector(".linklabel")).toBeNull();
    expect(plain?.textContent).toContain("example.org");

    unmount(app);
    host.remove();
    vi.unstubAllGlobals();
  });
});
