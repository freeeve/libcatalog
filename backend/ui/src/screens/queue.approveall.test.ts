// The bulk approve-all control on the review queue: a librarian-only,
// two-step (dry-run count -> confirm the count) button that kicks the async
// approve-all job. Fetch is fully injected; the session is a librarian so the
// control renders.
import { afterEach, describe, expect, it, vi, type Mock } from "vitest";
import { flushSync, mount, unmount } from "svelte";
import Queue from "./Queue.svelte";
import { sessionStore } from "../lib/stores";
import { setConfig } from "../lib/config";
import { invalidateAccess, loginLocal } from "../lib/auth";

let app: Record<string, unknown> | null = null;
let fetchMock: Mock;

async function tick(times = 12): Promise<void> {
  for (let i = 0; i < times; i++) {
    await Promise.resolve();
    flushSync();
  }
}

function jwtLike(): string {
  const body = btoa(JSON.stringify({ email: "lib@example.org", roles: ["librarian"] }))
    .replace(/\+/g, "-")
    .replace(/\//g, "_")
    .replace(/=+$/, "");
  return `hdr.${body}.sig`;
}

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status });
}

/** Mounts the queue with a librarian session and an empty PENDING page, then
 *  returns the fetch calls captured after mount so a test can assert on the
 *  approve-all requests only. */
async function mountQueue(): Promise<HTMLElement> {
  setConfig({ apiBase: "", localAuth: true, provider: "test", schemes: ["lcsh"] });
  sessionStorage.clear();
  localStorage.clear();
  invalidateAccess();
  fetchMock = vi.fn();
  vi.stubGlobal("fetch", fetchMock);
  fetchMock.mockResolvedValueOnce(json({ accessToken: jwtLike(), refreshToken: "r1", expiresIn: 900 }));
  await loginLocal("lib@example.org", "pw");
  sessionStore.set({ email: "lib@example.org", roles: ["librarian"] });

  // Every /v1/queue GET returns two pending rows; approve-all is routed per leg.
  fetchMock.mockImplementation((url: string, init?: RequestInit) => {
    if (url.startsWith("/v1/queue/approve-all")) {
      if (url.includes("confirm=")) {
        return Promise.resolve(json({ id: "job7", kind: "QUEUE_APPROVE", status: "QUEUED", requester: "lib@example.org", createdAt: "t" }, 202));
      }
      return Promise.resolve(json({ count: 2, confirmRequired: true }));
    }
    if (url.startsWith("/v1/queue")) {
      return Promise.resolve(json({ items: [], total: 0 }));
    }
    return Promise.resolve(json({}, 404));
  });

  const target = document.createElement("div");
  document.body.appendChild(target);
  app = mount(Queue, { target, props: {} });
  await tick();
  return target;
}

function buttonByText(host: HTMLElement, text: string): HTMLButtonElement | undefined {
  return [...host.querySelectorAll("button")].find((b) => b.textContent?.trim().startsWith(text)) as
    | HTMLButtonElement
    | undefined;
}

afterEach(() => {
  if (app) unmount(app);
  app = null;
  document.body.innerHTML = "";
  sessionStore.set(null);
  vi.unstubAllGlobals();
  setConfig(null);
});

describe("queue bulk approve-all", () => {
  it("dry-runs the filter, then confirms against the returned count and kicks the job", async () => {
    const host = await mountQueue();

    const trigger = buttonByText(host, "Approve all matching");
    expect(trigger).toBeTruthy();
    trigger!.click();
    await tick();

    // The dry run is a POST with no confirm param; the confirm bar shows its count.
    const dryRun = fetchMock.mock.calls.find((c) => (c[0] as string).startsWith("/v1/queue/approve-all"));
    expect(dryRun?.[1]?.method).toBe("POST");
    expect(dryRun?.[0]).not.toContain("confirm=");
    const confirmBar = host.querySelector(".approve-all-confirm");
    expect(confirmBar?.textContent).toContain("Approve all 2 pending suggestions");

    const confirm = buttonByText(host, "Approve 2");
    expect(confirm).toBeTruthy();
    confirm!.click();
    await tick();

    // The confirm leg carries confirm=2; the bar closes and a background notice shows.
    const confirmCall = fetchMock.mock.calls.find((c) => (c[0] as string).includes("confirm=2"));
    expect(confirmCall).toBeTruthy();
    expect(host.querySelector(".approve-all-confirm")).toBeNull();
    expect(host.querySelector(".notice")?.textContent).toContain("approving 2 suggestions in the background");
  });

  it("hides the control on non-PENDING views", async () => {
    const host = await mountQueue();
    // Switch the status filter to APPROVED; approve-all only acts on PENDING.
    const statusSelect = host.querySelector<HTMLSelectElement>("select");
    statusSelect!.value = "APPROVED";
    statusSelect!.dispatchEvent(new Event("change", { bubbles: true }));
    await tick();
    expect(buttonByText(host, "Approve all matching")).toBeUndefined();
  });
});
