// Axe audit over the Login screen and the WorkEditor's document renderer
// (WorkDocView) mounted with fixture data in jsdom. color-contrast needs a
// real rendering engine (canvas), so that single rule is skipped here; the
// palette in app.css is chosen for WCAG AA contrast.
import { afterEach, describe, expect, it } from "vitest";
import { mount, unmount, flushSync } from "svelte";
import axe from "axe-core";
import Login from "./screens/Login.svelte";
import WorkDocView from "./components/WorkDocView.svelte";
import type { WorkDoc } from "./lib/types";

const fixtureDoc: WorkDoc = {
  workId: "w-001",
  profileId: "work-monograph",
  work: {
    id: "w-001",
    fields: {
      title: [{ v: "The Sea Around Us", prov: "feed:overdrive", node: "_:t1" }],
      subjectLabels: [
        { v: "Ocean", prov: "enrichment:locsh", node: "_:s1", iri: false },
        { v: "Marine biology", prov: "editorial:", node: "_:s2" },
        { v: "Oceanography -- history", prov: "feed:marc", node: "_:s3", overridden: true },
      ],
      language: [{ v: "en", lang: "en", prov: "feed:overdrive", node: "_:l1" }],
    },
  },
  instances: [
    {
      id: "i-001",
      fields: {
        isbn: [{ v: "9780195069976", prov: "feed:overdrive", node: "_:i1" }],
      },
    },
  ],
  passthrough: [
    '<http://example.org/w-001> <http://example.org/p> "unclaimed" <feed:overdrive> .',
  ],
};

async function audit(node: Element): Promise<axe.AxeResults> {
  return axe.run(node, {
    rules: { "color-contrast": { enabled: false } },
  });
}

let cleanup: (() => void) | null = null;

afterEach(() => {
  cleanup?.();
  cleanup = null;
  document.body.innerHTML = "";
});

describe("a11y", () => {
  it("Login has no axe violations", async () => {
    const host = document.createElement("div");
    document.body.appendChild(host);
    const app = mount(Login, {
      target: host,
      props: {
        config: {
          apiBase: "",
          localAuth: true,
          oidc: { issuer: "https://issuer.example", clientId: "spa" },
          provider: "test",
        },
      },
    });
    cleanup = () => unmount(app);
    flushSync();
    const results = await audit(host);
    expect(results.violations).toEqual([]);
  });

  it("WorkEditor document view has no axe violations", async () => {
    // WorkDocView renders an <article>; give it the page's main landmark the
    // WorkEditor screen provides in the app.
    const host = document.createElement("main");
    document.body.appendChild(host);
    const app = mount(WorkDocView, { target: host, props: { doc: fixtureDoc, etag: "etag-1" } });
    cleanup = () => unmount(app);
    flushSync();
    const results = await audit(host);
    expect(results.violations).toEqual([]);
  });
});
