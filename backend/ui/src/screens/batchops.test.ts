// The Execute gate (tasks/113): a destructive batch execute is enabled only
// while the current inputs match what the last dry run previewed -- editing a
// param (or op/selection) after the dry run disables it until a fresh one.
import { afterEach, describe, expect, it, vi } from "vitest";
import { flushSync, mount, unmount } from "svelte";
import BatchOps from "./BatchOps.svelte";
import { invalidateAccess, loginLocal } from "../lib/auth";
import { setConfig } from "../lib/config";

let cleanup: (() => void) | null = null;
afterEach(() => cleanup?.());

async function tick(times = 4): Promise<void> {
  for (let i = 0; i < times; i++) {
    await Promise.resolve();
    flushSync();
  }
}

function jwtLike(): string {
  const body = btoa(
    JSON.stringify({ email: "staff@example.org", roles: ["librarian"] }),
  )
    .replace(/\+/g, "-")
    .replace(/\//g, "_")
    .replace(/=+$/, "");
  return `hdr.${body}.sig`;
}

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status });
}

const macro = {
  id: "m1",
  label: "Series summary",
  shared: true,
  owner: "staff@example.org",
  ops: [
    {
      resource: "work",
      path: "summary",
      action: "set",
      values: [{ v: "${series} book.", lang: "en" }],
    },
  ],
  params: [{ name: "series", label: "Series name" }],
  createdAt: "2026-07-01T00:00:00Z",
  updatedAt: "2026-07-01T00:00:00Z",
};

async function mountBatchOps(): Promise<{ host: HTMLElement }> {
  setConfig({
    apiBase: "",
    localAuth: true,
    provider: "test",
    schemes: ["lcsh"],
  });
  const fetchMock = vi.fn();
  vi.stubGlobal("fetch", fetchMock);
  fetchMock.mockResolvedValueOnce(
    json({ accessToken: jwtLike(), refreshToken: "r1", expiresIn: 900 }),
  );
  await loginLocal("staff@example.org", "pw");
  fetchMock.mockImplementation((url: string, init?: RequestInit) => {
    if (url.includes("/v1/profiles"))
      return Promise.resolve(
        json({
          profiles: {
            "work-monograph": {
              id: "work-monograph",
              label: "Work",
              resourceType: "work",
              fields: [
                {
                  path: "summary",
                  label: "Summary",
                  valueSource: { kind: "langLiteral" },
                },
              ],
            },
          },
        }),
      );
    if (url.includes("/v1/macros"))
      return Promise.resolve(json({ macros: [macro] }));
    if (url.includes("/v1/queries"))
      return Promise.resolve(json({ queries: [] }));
    if (url.includes("/v1/batch/ops") && init?.method === "POST")
      return Promise.resolve(
        json({
          dryRun: true,
          matched: 1,
          applied: 1,
          failed: 0,
          added: 1,
          removed: 0,
          results: [],
        }),
      );
    return Promise.resolve(json({}));
  });
  const host = document.createElement("div");
  document.body.appendChild(host);
  const app = mount(BatchOps, { target: host, props: { initialMacro: "m1" } });
  cleanup = () => {
    unmount(app);
    vi.unstubAllGlobals();
    setConfig(null);
    invalidateAccess();
    localStorage.clear();
    host.remove();
  };
  await tick(10);
  return { host };
}

function buttonByText(host: HTMLElement, text: string): HTMLButtonElement {
  const btn = [...host.querySelectorAll("button")].find(
    (b) => b.textContent?.trim() === text,
  );
  if (!btn) throw new Error(`no "${text}" button`);
  return btn;
}

describe("BatchOps execute gate", () => {
  it("requires a dry run of the exact current inputs", async () => {
    const { host } = await mountBatchOps();
    const execute = buttonByText(host, "Execute");
    expect(execute.disabled).toBe(true);

    // Fill the macro param, dry-run: Execute unlocks.
    const param = host.querySelector<HTMLInputElement>("#param-series")!;
    param.value = "Locked Tomb";
    param.dispatchEvent(new Event("input", { bubbles: true }));
    flushSync();
    buttonByText(host, "Dry run").click();
    await tick(10);
    expect(execute.disabled).toBe(false);

    // Editing the param after the dry run re-locks Execute until a fresh run.
    param.value = "Locked Tomb #2";
    param.dispatchEvent(new Event("input", { bubbles: true }));
    flushSync();
    expect(execute.disabled).toBe(true);

    buttonByText(host, "Dry run").click();
    await tick(10);
    expect(execute.disabled).toBe(false);
  });
});

describe("BatchOps profile picker (tasks/346)", () => {
  it("offers a work-profile selector when several exist and switches the field list", async () => {
    setConfig({
      apiBase: "",
      localAuth: true,
      provider: "test",
      schemes: ["lcsh"],
    });
    const fetchMock = vi.fn();
    vi.stubGlobal("fetch", fetchMock);
    fetchMock.mockResolvedValueOnce(
      json({ accessToken: jwtLike(), refreshToken: "r1", expiresIn: 900 }),
    );
    await loginLocal("staff@example.org", "pw");
    fetchMock.mockImplementation((url: string) => {
      if (url.includes("/v1/profiles"))
        return Promise.resolve(
          json({
            profiles: {
              "work-monograph": {
                id: "work-monograph",
                label: "Monograph",
                resourceType: "work",
                fields: [
                  {
                    path: "summary",
                    label: "Summary",
                    valueSource: { kind: "langLiteral" },
                  },
                ],
              },
              fastadd: {
                id: "fastadd",
                label: "Fast add",
                resourceType: "work",
                fields: [
                  {
                    path: "title",
                    label: "Title",
                    valueSource: { kind: "literal" },
                  },
                ],
              },
              "instance-ebook": {
                id: "instance-ebook",
                label: "Instance",
                resourceType: "instance",
                fields: [{ path: "isbn", label: "ISBN" }],
              },
            },
          }),
        );
      if (url.includes("/v1/macros"))
        return Promise.resolve(json({ macros: [] }));
      if (url.includes("/v1/queries"))
        return Promise.resolve(json({ queries: [] }));
      return Promise.resolve(json({}));
    });
    const host = document.createElement("div");
    document.body.appendChild(host);
    const app = mount(BatchOps, { target: host, props: {} });
    cleanup = () => {
      unmount(app);
      vi.unstubAllGlobals();
      setConfig(null);
      invalidateAccess();
      localStorage.clear();
      host.remove();
    };
    await tick(10);

    // The selector lists only WORK profiles (not the instance one), sorted by id.
    const sel = host.querySelector("#op-profile") as HTMLSelectElement;
    expect(sel).toBeTruthy();
    expect([...sel.options].map((o) => o.value)).toEqual([
      "fastadd",
      "work-monograph",
    ]);

    const fieldSel = host.querySelector(
      'select[aria-label="Field"]',
    ) as HTMLSelectElement;
    const workOptions = () =>
      [...fieldSel.querySelectorAll('optgroup[label="Work"] option')].map(
        (o) => o.textContent,
      );
    // Default work-monograph offers Summary.
    expect(workOptions()).toContain("Summary");

    // Switching to fastadd swaps the Work field list to its fields.
    sel.value = "fastadd";
    sel.dispatchEvent(new Event("change"));
    await tick(6);
    expect(workOptions()).toContain("Title");
    expect(workOptions()).not.toContain("Summary");
  });
});
