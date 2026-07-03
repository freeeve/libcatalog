// Axe audit over the Login screen, the editable WorkEditor (profile form,
// save bar, diff preview), the moderation Queue, and the VocabPicker modal,
// mounted with fixture data in jsdom. color-contrast needs a real rendering
// engine (canvas), so that single rule is skipped here; the palette in
// app.css is chosen for WCAG AA contrast.
import { afterEach, describe, expect, it, vi } from "vitest";
import { mount, unmount, flushSync } from "svelte";
import axe from "axe-core";
import Login from "./screens/Login.svelte";
import Queue from "./screens/Queue.svelte";
import WorkEditor from "./screens/WorkEditor.svelte";
import Authorities from "./screens/Authorities.svelte";
import AuthorityEditor from "./screens/AuthorityEditor.svelte";
import BatchOps from "./screens/BatchOps.svelte";
import Macros from "./screens/Macros.svelte";
import Exports from "./screens/Exports.svelte";
import CommandPalette from "./components/CommandPalette.svelte";
import MarcPanel from "./components/MarcPanel.svelte";
import CopyCat from "./screens/CopyCat.svelte";
import Duplicates from "./screens/Duplicates.svelte";
import VocabPicker from "./components/VocabPicker.svelte";
import KbdLegend from "./components/KbdLegend.svelte";
import { invalidateAccess, loginLocal } from "./lib/auth";
import { setConfig } from "./lib/config";
import { bindKeys, GLOBAL_SCOPE, pushScope, resetKeyboard } from "./lib/keyboard";
import { sessionStore } from "./lib/stores";
import type { QueuePage, Suggestion, WorkDoc } from "./lib/types";

const fixtureDoc: WorkDoc = {
  workId: "w-001",
  profileId: "work-monograph",
  work: {
    id: "w-001",
    fields: {
      title: [{ v: "The Sea Around Us", prov: "feed:overdrive", node: "_:t1" }],
      subjects: [
        { v: "http://id.loc.gov/sh-ocean", prov: "enrichment:locsh", node: "_:s1", iri: true },
        { v: "http://id.loc.gov/sh-marine", prov: "editorial:", node: "_:s2", iri: true },
        { v: "http://id.loc.gov/sh-oceanography", prov: "feed:marc", node: "_:s3", iri: true, overridden: true },
      ],
      summary: [{ v: "A natural history of the ocean.", lang: "en", prov: "feed:overdrive", node: "_:sm1" }],
      language: [{ v: "http://id.loc.gov/vocabulary/languages/eng", iri: true, prov: "feed:overdrive", node: "_:l1" }],
      subjectLabels: [{ v: "Ocean", prov: "enrichment:locsh", node: "_:x1" }],
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

const fixtureSuggestions: Suggestion[] = [
  {
    workId: "w1",
    term: { scheme: "lcsh", id: "http://id.loc.gov/sh1", label: "Sea monsters" },
    type: "ADD",
    status: "PENDING",
    supporterCount: 4,
    provenance: "PIPELINE",
    confidence: 0.91,
    workTitle: "The Sea Around Us",
    createdAt: "2026-06-01T00:00:00Z",
    lastActivityAt: "2026-06-02T00:00:00Z",
  },
  {
    workId: "w2",
    term: { scheme: "lcsh", id: "http://id.loc.gov/sh2", label: "Whales" },
    type: "REMOVE",
    status: "PENDING",
    supporterCount: 2,
    reasonCounts: { "off-topic": 2, offensive: 1 },
    provenance: "PATRON",
    workTitle: "Moby-Dick",
    createdAt: "2026-06-03T00:00:00Z",
    lastActivityAt: "2026-06-03T00:00:00Z",
  },
  {
    workId: "w3",
    term: { scheme: "folk", id: "cozy-fantasy", label: "cozy-fantasy" },
    type: "ADD",
    status: "PENDING",
    supporterCount: 7,
    provenance: "PATRON",
    workTitle: "Legends & Lattes",
    createdAt: "2026-06-04T00:00:00Z",
    lastActivityAt: "2026-06-04T00:00:00Z",
  },
];

async function audit(node: Element): Promise<axe.AxeResults> {
  return axe.run(node, {
    rules: { "color-contrast": { enabled: false } },
  });
}

/** Lets chained mocked-fetch awaits land, then flushes Svelte effects. */
async function tick(times = 4): Promise<void> {
  for (let i = 0; i < times; i++) await new Promise((r) => setTimeout(r, 0));
  flushSync();
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

/** Seeds a librarian session with fetch fully mocked; queue reads return the
 *  fixture page. Returns the cleanup for afterEach. */
async function mockStaffBackend(page: QueuePage): Promise<() => void> {
  setConfig({ apiBase: "", localAuth: true, provider: "test", schemes: ["lcsh", "fast", "folk"] });
  const fetchMock = vi.fn();
  vi.stubGlobal("fetch", fetchMock);
  fetchMock.mockResolvedValueOnce(json({ accessToken: jwtLike(), refreshToken: "r1", expiresIn: 900 }));
  await loginLocal("staff@example.org", "pw");
  fetchMock.mockImplementation(() => Promise.resolve(json(page)));
  sessionStore.set({ email: "staff@example.org", roles: ["librarian"] });
  return () => {
    vi.unstubAllGlobals();
    sessionStore.set(null);
    setConfig(null);
    invalidateAccess();
    localStorage.clear();
  };
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

  it("editable WorkEditor with staged ops and a diff preview has no axe violations", async () => {
    setConfig({ apiBase: "", localAuth: true, provider: "test", schemes: ["lcsh", "fast", "folk"] });
    const fetchMock = vi.fn();
    vi.stubGlobal("fetch", fetchMock);
    fetchMock.mockResolvedValueOnce(json({ accessToken: jwtLike(), refreshToken: "r1", expiresIn: 900 }));
    await loginLocal("staff@example.org", "pw");
    fetchMock.mockImplementation((url: string) => {
      if (url.includes("/doc")) return Promise.resolve(json({ etag: "etag-1", doc: fixtureDoc }));
      if (url === "/v1/drafts") return Promise.resolve(json({ drafts: [] }));
      if (url.includes("/ops"))
        return Promise.resolve(
          json({
            etag: "etag-1",
            diff: {
              added: ['<http://example.org/w-001> <https://github.com/freeeve/libcatalog/ns#overrides> "subjects" <editorial:> .'],
              removed: ['<http://example.org/w-001> <http://id.loc.gov/ontologies/bibframe/subject> <http://id.loc.gov/sh-ocean> <feed:marc> .'],
            },
          }),
        );
      return Promise.resolve(json({}));
    });

    const host = document.createElement("div");
    document.body.appendChild(host);
    const app = mount(WorkEditor, { target: host, props: { workId: "w-001" } });
    cleanup = () => {
      unmount(app);
      vi.unstubAllGlobals();
      setConfig(null);
      resetKeyboard();
      invalidateAccess();
      localStorage.clear();
    };
    await tick();
    expect(host.textContent).toContain("The Sea Around Us");

    // Stage a removal so the pending styling and the save bar join the tree.
    const removeBtn = [...host.querySelectorAll("button")].find((b) => b.textContent?.trim() === "Remove");
    expect(removeBtn).toBeDefined();
    removeBtn?.click();
    flushSync();
    expect(host.textContent).toContain("1 staged edit");

    // Dry-run preview so DiffPreview is audited too.
    const previewBtn = [...host.querySelectorAll("button")].find((b) => b.textContent?.trim() === "Preview changes");
    previewBtn?.click();
    await tick();
    expect(host.textContent).toContain("1 added");

    const results = await audit(host);
    expect(results.violations).toEqual([]);
  });

  it("Queue with rows and staged decisions has no axe violations", async () => {
    const restore = await mockStaffBackend({ items: fixtureSuggestions, cursor: "c1" });
    const host = document.createElement("div");
    document.body.appendChild(host);
    const app = mount(Queue, { target: host });
    cleanup = () => {
      unmount(app);
      restore();
    };
    flushSync();
    await new Promise((r) => setTimeout(r, 0)); // let the mocked queue load land
    flushSync();
    expect(host.textContent).toContain("The Sea Around Us");
    // Stage one decision so the publish bar is part of the audited tree.
    const approveBtn = [...host.querySelectorAll("button")].find((b) => b.textContent?.trim() === "Approve");
    approveBtn?.click();
    flushSync();
    expect(host.textContent).toContain("staged");
    const results = await audit(host);
    expect(results.violations).toEqual([]);
  });

  it("Authorities search with results and the create affordance has no axe violations", async () => {
    setConfig({ apiBase: "", localAuth: true, provider: "test", schemes: ["lcsh", "local"] });
    const fetchMock = vi.fn();
    vi.stubGlobal("fetch", fetchMock);
    fetchMock.mockResolvedValueOnce(json({ accessToken: jwtLike(), refreshToken: "r1", expiresIn: 900 }));
    await loginLocal("staff@example.org", "pw");
    fetchMock.mockImplementation(() =>
      Promise.resolve(
        json({
          terms: [
            {
              scheme: "local",
              id: "https://github.com/freeeve/libcatalog/authority/a0123456789ab",
              labels: { en: "Cozy fantasy" },
              altLabels: { en: ["Comfort fantasy"] },
            },
            {
              scheme: "local",
              id: "https://github.com/freeeve/libcatalog/authority/a0123456789ac",
              labels: { en: "Trans folks" },
              mergedInto: "https://homosaurus.org/v4/homoit0001235",
            },
          ],
        }),
      ),
    );
    const host = document.createElement("div");
    document.body.appendChild(host);
    const app = mount(Authorities, { target: host });
    cleanup = () => {
      unmount(app);
      vi.unstubAllGlobals();
      setConfig(null);
      resetKeyboard();
      invalidateAccess();
      localStorage.clear();
    };
    await tick();
    expect(host.textContent).toContain("Cozy fantasy");
    expect(host.textContent).toContain("merged");
    // Type an unmatched heading so the create button joins the audited tree.
    const input = host.querySelector<HTMLInputElement>("#auth-q");
    expect(input).not.toBeNull();
    input!.value = "Bone magic";
    input!.dispatchEvent(new Event("input"));
    await new Promise((r) => setTimeout(r, 300)); // debounce
    await tick();
    expect(host.textContent).toContain("Create local authority");
    const results = await audit(host);
    expect(results.violations).toEqual([]);
  });

  it("AuthorityEditor with a retired term and the merge tool has no axe violations", async () => {
    setConfig({ apiBase: "", localAuth: true, provider: "test", schemes: ["lcsh", "local"] });
    const fetchMock = vi.fn();
    vi.stubGlobal("fetch", fetchMock);
    fetchMock.mockResolvedValueOnce(json({ accessToken: jwtLike(), refreshToken: "r1", expiresIn: 900 }));
    await loginLocal("staff@example.org", "pw");
    fetchMock.mockImplementation((url: string) => {
      if (url.includes("/profile"))
        return Promise.resolve(
          json({
            id: "authority-topic",
            label: "Authority (topical term)",
            resourceType: "authority",
            fields: [
              { path: "prefLabel", label: "Preferred label" },
              { path: "broader", label: "Broader terms" },
              { path: "exactMatch", label: "Exact match (external vocabulary)", help: "URI of the same concept." },
            ],
          }),
        );
      return Promise.resolve(
        json({
          id: "a0123456789ab",
          etag: "etag-1",
          term: {
            uri: "https://github.com/freeeve/libcatalog/authority/a0123456789ab",
            prefLabel: { en: "Cozy fantasy", es: "Fantasía acogedora" },
            altLabel: { en: ["Comfort fantasy"] },
            definition: { en: "Low-stakes fantasy centered on comfort." },
            broader: ["https://example.org/vocab/fantasy"],
            exactMatch: ["http://id.loc.gov/authorities/subjects/sh1"],
          },
        }),
      );
    });
    const host = document.createElement("div");
    document.body.appendChild(host);
    const app = mount(AuthorityEditor, { target: host, props: { authorityId: "a0123456789ab" } });
    cleanup = () => {
      unmount(app);
      vi.unstubAllGlobals();
      setConfig(null);
      resetKeyboard();
      invalidateAccess();
      localStorage.clear();
    };
    await tick();
    expect(host.textContent).toContain("Cozy fantasy");
    expect(host.textContent).toContain("Exact match (external vocabulary)");
    const results = await audit(host);
    expect(results.violations).toEqual([]);
  });

  it("BatchOps with a macro, params, and a run result has no axe violations", async () => {
    setConfig({ apiBase: "", localAuth: true, provider: "test", schemes: ["lcsh"] });
    const fetchMock = vi.fn();
    vi.stubGlobal("fetch", fetchMock);
    fetchMock.mockResolvedValueOnce(json({ accessToken: jwtLike(), refreshToken: "r1", expiresIn: 900 }));
    await loginLocal("staff@example.org", "pw");
    const macro = {
      id: "m1",
      label: "Series summary",
      keys: "1",
      shared: true,
      owner: "staff@example.org",
      ops: [{ resource: "work", path: "summary", action: "set", values: [{ v: "${series} book.", lang: "en" }] }],
      params: [{ name: "series", label: "Series name" }],
      createdAt: "2026-07-01T00:00:00Z",
      updatedAt: "2026-07-01T00:00:00Z",
    };
    fetchMock.mockImplementation((url: string, init?: RequestInit) => {
      if (url.includes("/v1/profiles"))
        return Promise.resolve(
          json({
            profiles: {
              "work-monograph": {
                id: "work-monograph",
                label: "Work",
                resourceType: "work",
                fields: [{ path: "summary", label: "Summary", valueSource: { kind: "langLiteral" } }],
              },
            },
          }),
        );
      if (url.includes("/v1/macros")) return Promise.resolve(json({ macros: [macro] }));
      if (url.includes("/v1/queries")) return Promise.resolve(json({ queries: [] }));
      if (url.includes("/v1/batch/resolve"))
        return Promise.resolve(json({ matched: 2, works: [{ workId: "w1", title: "Gideon the Ninth" }] }));
      if (url.includes("/v1/batch/ops") && init?.method === "POST")
        return Promise.resolve(
          json({
            dryRun: true,
            matched: 2,
            applied: 1,
            failed: 1,
            added: 2,
            removed: 0,
            results: [
              { workId: "w1", diff: { added: ["<#w1Work> <p> \"x\" <editorial:> ."], removed: [] } },
              { workId: "w2", error: "no such work" },
            ],
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
    };
    await tick();
    expect(host.textContent).toContain("Series summary");
    expect(host.textContent).toContain("Series name"); // param prompt rendered
    // Resolve a selection and dry-run so the results list joins the tree.
    const previewBtn = [...host.querySelectorAll("button")].find((b) => b.textContent?.includes("Preview selection"));
    previewBtn?.click();
    await tick();
    expect(host.textContent).toContain("Gideon the Ninth");
    const dryBtn = [...host.querySelectorAll("button")].find((b) => b.textContent?.trim() === "Dry run");
    dryBtn?.click();
    await tick();
    expect(host.textContent).toContain("no such work");
    const results = await audit(host);
    expect(results.violations).toEqual([]);
  });

  it("Macros list and editor have no axe violations", async () => {
    setConfig({ apiBase: "", localAuth: true, provider: "test", schemes: ["lcsh"] });
    const fetchMock = vi.fn();
    vi.stubGlobal("fetch", fetchMock);
    fetchMock.mockResolvedValueOnce(json({ accessToken: jwtLike(), refreshToken: "r1", expiresIn: 900 }));
    await loginLocal("staff@example.org", "pw");
    fetchMock.mockImplementation(() =>
      Promise.resolve(
        json({
          macros: [
            {
              id: "m1",
              label: "Series summary",
              keys: "1",
              shared: true,
              owner: "staff@example.org",
              ops: [{ resource: "work", path: "summary", action: "set", values: [{ v: "${series} book.", lang: "en" }] }],
              params: [{ name: "series", label: "Series name" }],
              createdAt: "2026-07-01T00:00:00Z",
              updatedAt: "2026-07-01T00:00:00Z",
            },
          ],
        }),
      ),
    );
    sessionStore.set({ email: "staff@example.org", roles: ["librarian"] });
    const host = document.createElement("div");
    document.body.appendChild(host);
    const app = mount(Macros, { target: host });
    cleanup = () => {
      unmount(app);
      vi.unstubAllGlobals();
      setConfig(null);
      sessionStore.set(null);
      invalidateAccess();
      localStorage.clear();
    };
    await tick();
    expect(host.textContent).toContain("Series summary");
    // Open the editor pane so its form is part of the audited tree.
    const editBtn = [...host.querySelectorAll("button")].find((b) => b.textContent?.trim() === "Edit");
    editBtn?.click();
    flushSync();
    expect(host.textContent).toContain("Parameters");
    const results = await audit(host);
    expect(results.violations).toEqual([]);
  });

  it("Exports with the MARC lossiness note and a job table has no axe violations", async () => {
    setConfig({ apiBase: "", localAuth: true, provider: "test", schemes: ["lcsh"] });
    const fetchMock = vi.fn();
    vi.stubGlobal("fetch", fetchMock);
    fetchMock.mockResolvedValueOnce(json({ accessToken: jwtLike(), refreshToken: "r1", expiresIn: 900 }));
    await loginLocal("staff@example.org", "pw");
    fetchMock.mockImplementation((url: string) => {
      if (url.includes("/v1/exports"))
        return Promise.resolve(
          json({
            exports: [
              {
                id: "j1",
                requester: "staff@example.org",
                format: "csv",
                selection: { workIds: ["w1", "w2"] },
                status: "DONE",
                records: 2,
                createdAt: "2026-07-01T00:00:00Z",
                expiresAt: "2099-01-01T00:00:00Z",
                downloadUrl: "/v1/exports/j1/download?token=t",
              },
              {
                id: "j2",
                requester: "staff@example.org",
                format: "marc",
                selection: { all: true },
                status: "DONE",
                records: 9,
                createdAt: "2026-06-01T00:00:00Z",
                expiresAt: "2026-06-02T00:00:00Z", // long past: renders expired
              },
              {
                id: "j3",
                requester: "staff@example.org",
                format: "nquads",
                selection: { workIds: ["w3"] },
                status: "QUEUED",
                createdAt: "2026-07-01T01:00:00Z",
              },
            ],
          }),
        );
      if (url.includes("/v1/queries")) return Promise.resolve(json({ queries: [] }));
      if (url.includes("/v1/batch/resolve")) return Promise.resolve(json({ matched: 2, works: [] }));
      return Promise.resolve(json({}));
    });
    const host = document.createElement("div");
    document.body.appendChild(host);
    const app = mount(Exports, { target: host, props: { initialKind: "search", initialQuery: "ninth" } });
    cleanup = () => {
      unmount(app);
      vi.unstubAllGlobals();
      setConfig(null);
      invalidateAccess();
      localStorage.clear();
    };
    await tick();
    expect(host.textContent).toContain("2 works"); // deep-link preview resolved
    expect(host.textContent).toContain("EXPIRED");
    expect(host.textContent).toContain("working…");
    // The MARC option surfaces the lossiness note + fidelity link.
    const formatSel = host.querySelector<HTMLSelectElement>("#ex-format");
    formatSel!.value = "marc";
    formatSel!.dispatchEvent(new Event("change"));
    flushSync();
    expect(host.textContent).toContain("Lossy");
    expect(host.querySelector('a[href*="marc-fidelity"]')).not.toBeNull();
    const results = await audit(host);
    expect(results.violations).toEqual([]);
  });

  it("MarcPanel grid with a lossy field and fixed-field builder has no axe violations", async () => {
    setConfig({ apiBase: "", localAuth: true, provider: "test", schemes: ["lcsh"] });
    const fetchMock = vi.fn();
    vi.stubGlobal("fetch", fetchMock);
    fetchMock.mockResolvedValueOnce(json({ accessToken: jwtLike(), refreshToken: "r1", expiresIn: 900 }));
    await loginLocal("staff@example.org", "pw");
    fetchMock.mockImplementation(() =>
      Promise.resolve(
        json({
          workId: "w-001",
          etag: "etag-1",
          knownLoss: { "037": "vendor convention: decodes as an 024-shaped identifier" },
          records: [
            {
              node: "#i1Instance",
              leader: "00000nam a2200000   4500",
              fields: [
                { tag: "001", value: "ODN0001" },
                { tag: "008", value: "240702s2026    nyu           000 1 eng d" },
                {
                  tag: "245",
                  ind1: "1",
                  ind2: "0",
                  subfields: [
                    { code: "a", value: "Gideon the Ninth" },
                    { code: "c", value: "Tamsyn Muir." },
                  ],
                },
                {
                  tag: "037",
                  ind1: " ",
                  ind2: " ",
                  subfields: [{ code: "a", value: "12345-67" }],
                  lossy: "vendor convention: decodes as an 024-shaped identifier",
                },
              ],
            },
          ],
        }),
      ),
    );
    const host = document.createElement("div");
    document.body.appendChild(host);
    const app = mount(MarcPanel, { target: host, props: { workId: "w-001" } });
    cleanup = () => {
      unmount(app);
      vi.unstubAllGlobals();
      setConfig(null);
      invalidateAccess();
      localStorage.clear();
    };
    await tick();
    expect(host.textContent).toContain("Crosswalk-lossy tag");
    expect(host.querySelector('a[href*="marc-fidelity"]')).not.toBeNull();
    // Expand the 008 positional builder so it joins the audited tree.
    const posBtn = [...host.querySelectorAll("button")].filter((b) => b.textContent?.trim() === "Positions")[1];
    expect(posBtn).toBeDefined();
    posBtn?.click();
    flushSync();
    expect(host.textContent).toContain("Date entered");
    const results = await audit(host);
    expect(results.violations).toEqual([]);
  });

  it("CopyCat with search results and a staged batch review has no axe violations", async () => {
    setConfig({ apiBase: "", localAuth: true, provider: "test", schemes: ["lcsh"] });
    const fetchMock = vi.fn();
    vi.stubGlobal("fetch", fetchMock);
    fetchMock.mockResolvedValueOnce(json({ accessToken: jwtLike(), refreshToken: "r1", expiresIn: 900 }));
    await loginLocal("staff@example.org", "pw");
    const staged = {
      batch: {
        id: "b1",
        label: "search: gideon",
        source: "search",
        policy: "replace-feed",
        status: "STAGED",
        records: 2,
        owner: "staff@example.org",
        createdAt: "2026-07-01T00:00:00Z",
      },
      records: [
        {
          index: 0,
          title: "Gideon the Ninth",
          record: { node: "", leader: "", fields: [] },
          match: { matchedWork: true, matchedInstance: false, workId: "wabc123def456" },
          decision: "import",
        },
        {
          index: 1,
          title: "Harrow the Ninth",
          record: { node: "", leader: "", fields: [] },
          match: { matchedWork: false, matchedInstance: false },
          decision: "import",
        },
      ],
    };
    fetchMock.mockImplementation((url: string) => {
      if (url.includes("/v1/copycat/targets"))
        return Promise.resolve(json({ targets: [{ name: "loc", url: "http://lx2.loc.gov:210/LCDB", protocol: "sru" }] }));
      if (url.includes("/v1/copycat/search"))
        return Promise.resolve(
          json({
            results: [
              {
                target: "loc",
                title: "Gideon the Ninth",
                author: "Muir, Tamsyn",
                date: "2019",
                isbn: "9781250313195",
                record: { node: "", leader: "", fields: [] },
              },
            ],
            failures: { flaky: "connection refused" },
          }),
        );
      if (url.match(/\/v1\/copycat\/batches\/b1$/)) return Promise.resolve(json(staged));
      if (url.includes("/v1/copycat/batches")) return Promise.resolve(json({ batches: [staged.batch] }));
      return Promise.resolve(json({}));
    });
    sessionStore.set({ email: "staff@example.org", roles: ["librarian"] });
    const host = document.createElement("div");
    document.body.appendChild(host);
    const app = mount(CopyCat, { target: host });
    cleanup = () => {
      unmount(app);
      vi.unstubAllGlobals();
      setConfig(null);
      sessionStore.set(null);
      invalidateAccess();
      localStorage.clear();
    };
    await tick();
    // Run a search so results + per-target failure join the tree.
    const input = host.querySelector<HTMLInputElement>('input[aria-label="Search query"]');
    input!.value = "gideon";
    input!.dispatchEvent(new Event("input"));
    flushSync();
    const searchBtn = [...host.querySelectorAll("button")].find((b) => b.textContent?.trim() === "Search");
    searchBtn?.click();
    await tick();
    expect(host.textContent).toContain("Muir, Tamsyn");
    expect(host.textContent).toContain("connection refused");
    // Open the staged batch so the match banner and review controls render.
    const batchBtn = [...host.querySelectorAll("button")].find((b) => b.textContent?.includes("search: gideon"));
    batchBtn?.click();
    await tick();
    expect(host.textContent).toContain("would merge with an existing work");
    expect(host.textContent).toContain("wabc123def456");
    const results = await audit(host);
    expect(results.violations).toEqual([]);
  });

  it("Duplicates with an expanded compare table has no axe violations", async () => {
    setConfig({ apiBase: "", localAuth: true, provider: "test", schemes: ["lcsh"] });
    const fetchMock = vi.fn();
    vi.stubGlobal("fetch", fetchMock);
    fetchMock.mockResolvedValueOnce(json({ accessToken: jwtLike(), refreshToken: "r1", expiresIn: 900 }));
    await loginLocal("staff@example.org", "pw");
    fetchMock.mockImplementation((url: string) => {
      if (url.includes("/v1/duplicates"))
        return Promise.resolve(
          json({
            groups: [
              {
                key: "muir tamsyngideon the nintheng",
                works: [
                  { workId: "wdupa0000001", title: "Gideon the Ninth" },
                  { workId: "wdupb0000001", title: "Gideon the Ninth" },
                ],
              },
            ],
          }),
        );
      if (url.includes("/doc"))
        return Promise.resolve(
          json({
            etag: "e1",
            doc: {
              workId: "w",
              profileId: "work-monograph",
              work: { id: "w", fields: { title: [{ v: "Gideon the Ninth", prov: "feed:marc", node: "_:t" }] } },
              instances: [],
              passthrough: [],
            },
          }),
        );
      return Promise.resolve(json({}));
    });
    const host = document.createElement("div");
    document.body.appendChild(host);
    const app = mount(Duplicates, { target: host });
    cleanup = () => {
      unmount(app);
      vi.unstubAllGlobals();
      setConfig(null);
      invalidateAccess();
      localStorage.clear();
    };
    await tick();
    const groupBtn = [...host.querySelectorAll("button")].find((b) => b.textContent?.includes("Gideon the Ninth"));
    groupBtn?.click();
    await tick();
    expect(host.textContent).toContain("keep");
    expect(host.textContent).toContain("Merge into");
    const results = await audit(host);
    expect(results.violations).toEqual([]);
  });

  it("CommandPalette has no axe violations", async () => {
    setConfig({ apiBase: "", localAuth: true, provider: "test", schemes: ["lcsh"] });
    const fetchMock = vi.fn(() => Promise.resolve(json({ macros: [], works: [], total: 0 })));
    vi.stubGlobal("fetch", fetchMock);
    const host = document.createElement("div");
    document.body.appendChild(host);
    const app = mount(CommandPalette, { target: host, props: { onclose: () => undefined } });
    cleanup = () => {
      unmount(app);
      vi.unstubAllGlobals();
      setConfig(null);
      resetKeyboard();
    };
    await tick();
    expect(host.querySelector('[role="dialog"]')).not.toBeNull();
    expect(host.textContent).toContain("Go to Works");
    const results = await audit(host);
    expect(results.violations).toEqual([]);
  });

  it("KbdLegend footer with scope bindings has no axe violations", async () => {
    bindKeys("works", {
      j: { description: "next result", legend: "move", keyLabel: "j/k", handler: () => undefined },
      k: { description: "previous result", hidden: true, handler: () => undefined },
    });
    bindKeys(GLOBAL_SCOPE, {
      "g w": { description: "go to works", legend: "go to screen", handler: () => undefined },
    });
    pushScope("works");
    const host = document.createElement("div");
    document.body.appendChild(host);
    const app = mount(KbdLegend, { target: host });
    cleanup = () => {
      unmount(app);
      resetKeyboard();
    };
    flushSync();
    expect(host.textContent).toContain("j/k move");
    expect(host.textContent).not.toContain("previous result");
    expect(host.textContent).toContain("go to screen");
    const results = await audit(host);
    expect(results.violations).toEqual([]);
  });

  it("VocabPicker modal has no axe violations", async () => {
    setConfig({ apiBase: "", localAuth: true, provider: "test", schemes: ["lcsh", "fast", "folk"] });
    const fetchMock = vi.fn(() => Promise.resolve(json({ terms: [] })));
    vi.stubGlobal("fetch", fetchMock);
    const host = document.createElement("div");
    document.body.appendChild(host);
    const app = mount(VocabPicker, {
      target: host,
      props: { onselect: () => undefined, onclose: () => undefined },
    });
    cleanup = () => {
      unmount(app);
      vi.unstubAllGlobals();
      setConfig(null);
    };
    flushSync();
    expect(host.querySelector('[role="dialog"]')).not.toBeNull();
    const results = await audit(host);
    expect(results.violations).toEqual([]);
  });
});
