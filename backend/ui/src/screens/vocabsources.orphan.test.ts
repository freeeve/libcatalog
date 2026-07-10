// tasks/255: the Vocabularies screen offered an orphan install two buttons that
// could only 404. An orphan row is synthesized from a snapshot with no source
// record behind it (an offline vocab-install, or a registry reset), so Upload and
// Delete source have nothing to act on -- both reach GetSource and answer
// "no such source". Remove is the one control that works, and it is what Views
// synthesizes the row for.
//
// The screen now reads the server's `orphan` marker. It cannot be derived on the
// client: an empty snapshotUrl means "upload-only source", which is a perfectly
// ordinary registered source, so the upload-only row below is the control that
// keeps this test honest.
import { afterEach, describe, expect, it, vi } from "vitest";
import { flushSync, mount, unmount } from "svelte";
import VocabSources from "./VocabSources.svelte";
import { invalidateAccess, loginLocal, session } from "../lib/auth";
import { setConfig } from "../lib/config";
import { sessionStore } from "../lib/stores";
import type { VocabSourceView } from "../lib/types";

let cleanup: (() => void) | null = null;
afterEach(() => {
  cleanup?.();
  cleanup = null;
  invalidateAccess();
  vi.unstubAllGlobals();
});

async function tick(times = 8): Promise<void> {
  for (let i = 0; i < times; i++) {
    await Promise.resolve();
    flushSync();
  }
}

function adminJWT(): string {
  const body = btoa(JSON.stringify({ email: "eve@example.org", roles: ["admin", "librarian"] }))
    .replace(/\+/g, "-")
    .replace(/\//g, "_")
    .replace(/=+$/, "");
  return `hdr.${body}.sig`;
}

const installedAt = "2026-07-10T00:00:00Z";

/** An install with no source record: only Remove can act on it. */
const orphan: VocabSourceView = {
  name: "zzorph",
  scheme: "zzorph",
  installed: { source: "zzorph", scheme: "zzorph", terms: 1, installedAt, snapshotUrl: "upload" },
  orphan: true,
};

/** Registered, installed, and with no snapshotUrl -- the InstallUpload escape
 *  hatch. Identical to the orphan over the wire but for the marker. */
const uploadOnly: VocabSourceView = {
  name: "zzupload",
  scheme: "zzupload",
  installed: { source: "zzupload", scheme: "zzupload", terms: 1, installedAt, snapshotUrl: "upload" },
};

/** Labels of the action buttons rendered in one row. */
function actionsFor(root: HTMLElement, name: string): string[] {
  const rows = [...root.querySelectorAll("tbody tr")];
  const row = rows.find((r) => r.querySelector(".name")?.textContent?.trim() === name);
  if (!row) throw new Error(`no row for ${name}; rows: ${rows.map((r) => r.querySelector(".name")?.textContent).join()}`);
  const cell = row.querySelector("td.actions");
  if (!cell) throw new Error(`row ${name} has no actions cell`);
  return [...cell.querySelectorAll("button, label")].map((el) => (el.textContent ?? "").trim().replace(/\s+/g, " "));
}

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status });
}

async function screen(sources: VocabSourceView[]): Promise<HTMLElement> {
  setConfig({ apiBase: "", localAuth: true, provider: "test", schemes: ["lcsh"] });
  const fetchMock = vi.fn();
  vi.stubGlobal("fetch", fetchMock);
  fetchMock.mockResolvedValueOnce(json({ accessToken: adminJWT(), refreshToken: "r1", expiresIn: 900 }));
  await loginLocal("eve@example.org", "pw");
  // App.svelte publishes the session; mounting the screen alone does not, and
  // without it the admin-gated buttons never render and the test proves nothing.
  sessionStore.set(session());
  fetchMock.mockImplementation((url: string) =>
    Promise.resolve(url.includes("/v1/vocabsources") ? json({ sources }) : json({})),
  );

  const target = document.createElement("div");
  document.body.appendChild(target);
  const app = mount(VocabSources, { target });
  cleanup = () => {
    unmount(app);
    sessionStore.set(null);
    setConfig(null);
    localStorage.clear();
    target.remove();
  };
  await tick(10);
  return target;
}

describe("an orphan install", () => {
  it("offers only the action that can succeed", async () => {
    const root = await screen([orphan, uploadOnly]);

    // The control. An upload-only source is a registered source: every action
    // works on it, and it is indistinguishable from the orphan except for the
    // marker. Without this, hiding the buttons on *every* installed row passes.
    const registered = actionsFor(root, "zzupload");
    expect(registered).toContain("Remove");
    expect(registered.some((a) => a.startsWith("Upload"))).toBe(true);
    expect(registered).toContain("Delete source");

    const dead = actionsFor(root, "zzorph");
    expect(dead).toEqual(["Remove"]);
  });

  it("says why, rather than silently dropping the buttons", async () => {
    const root = await screen([orphan, uploadOnly]);
    const rows = [...root.querySelectorAll("tbody tr")];
    const badges = rows.map((r) => [...r.querySelectorAll(".badge--orphan")].length);
    // Exactly one row is marked, and it is the orphan.
    expect(badges).toEqual([orphan.name < uploadOnly.name ? 1 : 0, orphan.name < uploadOnly.name ? 0 : 1]);
    expect(root.querySelector(".badge--orphan")?.textContent?.trim()).toBe("unregistered");
  });
});
