// The suggestion-policy editor (the UI half of the backend policy):
// it loads the stored policy and the registered vocabularies, and a save PUTs
// exactly the edited {enabled, freeText, schemes}.
import { afterEach, describe, expect, it, vi } from "vitest";
import { flushSync, mount, unmount } from "svelte";
import Suggestions from "./Suggestions.svelte";
import { invalidateAccess, loginLocal } from "../lib/auth";
import { setConfig } from "../lib/config";

let cleanup: (() => void) | null = null;
afterEach(() => cleanup?.());

async function tick(times = 12): Promise<void> {
  for (let i = 0; i < times; i++) {
    await Promise.resolve();
    flushSync();
  }
}

function jwtLike(): string {
  const body = btoa(
    JSON.stringify({ email: "admin@example.org", roles: ["admin"] }),
  )
    .replace(/\+/g, "-")
    .replace(/\//g, "_")
    .replace(/=+$/, "");
  return `hdr.${body}.sig`;
}

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status });
}

async function mountScreen(
  policy: unknown,
): Promise<{ host: HTMLElement; puts: unknown[] }> {
  setConfig({
    apiBase: "",
    localAuth: true,
    provider: "test",
    schemes: ["lcsh"],
  });
  const puts: unknown[] = [];
  const fetchMock = vi.fn();
  vi.stubGlobal("fetch", fetchMock);
  fetchMock.mockResolvedValueOnce(
    json({ accessToken: jwtLike(), refreshToken: "r1", expiresIn: 900 }),
  );
  await loginLocal("admin@example.org", "pw");
  fetchMock.mockImplementation((url: string, init?: RequestInit) => {
    if (url.includes("/v1/config/suggestions") && init?.method === "PUT") {
      const sent = JSON.parse(init.body as string);
      puts.push(sent);
      return Promise.resolve(json(sent)); // echo back the normalized policy
    }
    if (url.includes("/v1/config/suggestions"))
      return Promise.resolve(json(policy));
    if (url.includes("/v1/vocabsources"))
      return Promise.resolve(
        json({
          sources: [
            { name: "hs", scheme: "homosaurus" },
            { name: "lc", scheme: "lcsh" },
          ],
        }),
      );
    return Promise.resolve(json({}));
  });
  const host = document.createElement("div");
  document.body.appendChild(host);
  const app = mount(Suggestions, { target: host });
  cleanup = () => {
    unmount(app);
    vi.unstubAllGlobals();
    setConfig(null);
    invalidateAccess();
    localStorage.clear();
    host.remove();
  };
  await tick();
  return { host, puts };
}

function schemeBox(host: HTMLElement, scheme: string): HTMLInputElement {
  const labels = Array.from(host.querySelectorAll(".schemes label"));
  const label = labels.find((l) => l.textContent?.includes(scheme));
  const box = label?.querySelector("input[type=checkbox]") as
    HTMLInputElement | undefined;
  if (!box) throw new Error(`no scheme checkbox for ${scheme}`);
  return box;
}

describe("Suggestion policy editor", () => {
  it("reflects the stored policy and lists loaded vocabularies", async () => {
    const { host } = await mountScreen({
      enabled: false,
      freeText: "off",
      schemes: [],
    });
    const enabled = host.querySelector(".toggle input") as HTMLInputElement;
    expect(enabled.checked).toBe(false);
    // Options come from the registered vocabularies.
    const schemes = Array.from(host.querySelectorAll(".schemes label")).map(
      (l) => l.textContent?.trim(),
    );
    expect(schemes).toEqual(["homosaurus", "lcsh"]);
    // The config surface is disabled while suggestions are off.
    expect(
      (host.querySelector("fieldset") as HTMLFieldSetElement).disabled,
    ).toBe(true);
  });

  it("saves exactly the edited enabled, free-text mode, and allowlist", async () => {
    const { host, puts } = await mountScreen({
      enabled: false,
      freeText: "off",
      schemes: [],
    });
    (host.querySelector(".toggle input") as HTMLInputElement).click(); // enable
    await tick();
    (
      host.querySelector("input[name=freetext][value=any]") as HTMLInputElement
    ).click();
    schemeBox(host, "lcsh").click();
    await tick();
    (host.querySelector("button[type=submit]") as HTMLButtonElement).click();
    await tick();
    expect(puts).toHaveLength(1);
    expect(puts[0]).toEqual({
      enabled: true,
      freeText: "any",
      schemes: ["lcsh"],
    });
    expect(host.querySelector(".status .ok")?.textContent).toContain("Saved");
  });

  it("shows a saved scheme even when the registry does not list it", async () => {
    const { host } = await mountScreen({
      enabled: true,
      freeText: "existing",
      schemes: ["fast"],
    });
    const schemes = Array.from(host.querySelectorAll(".schemes label")).map(
      (l) => l.textContent?.trim(),
    );
    expect(schemes).toContain("fast"); // unioned in from the policy
    expect(schemeBox(host, "fast").checked).toBe(true);
    // free-text mode reflects the stored value
    expect(
      (
        host.querySelector(
          "input[name=freetext][value=existing]",
        ) as HTMLInputElement
      ).checked,
    ).toBe(true);
  });
});
