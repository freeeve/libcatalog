// Editor-session flows with fetch fully injected: the 412 conflict-and-
// rebase path, draft autosave create/update/delete, and draft resume.
import { afterEach, beforeEach, describe, expect, it, vi, type Mock } from "vitest";
import { get } from "svelte/store";
import { createEditorSession, AUTOSAVE_MS, type EditorSession } from "./editor";
import { invalidateAccess, loginLocal } from "./auth";
import { setConfig } from "./config";
import type { Op, WorkDoc } from "./types";

const doc: WorkDoc = {
  workId: "w1",
  profileId: "work-monograph",
  work: { id: "w1", fields: { title: [{ v: "The Sea Around Us", prov: "feed:overdrive", node: "_:t1" }] } },
  instances: [],
  passthrough: [],
};

const op: Op = { resource: "work", path: "tags", action: "add", value: { v: "sea" } };

function jwtLike(): string {
  const body = btoa(JSON.stringify({ email: "a@b.co", roles: ["librarian"] }))
    .replace(/\+/g, "-")
    .replace(/\//g, "_")
    .replace(/=+$/, "");
  return `hdr.${body}.sig`;
}

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status });
}

let fetchMock: Mock;
let session: EditorSession | null = null;

async function seedSession(): Promise<void> {
  fetchMock.mockResolvedValueOnce(json({ accessToken: jwtLike(), refreshToken: "r1", expiresIn: 900 }));
  await loginLocal("a@b.co", "pw");
  fetchMock.mockClear();
}

/** Loads a session for w1: doc fetch (etag e1) then the work's one draft slot.
    load() point-reads GET /v1/drafts/w1, so the mock returns w1's
    draft from the given set, or a 404 when there is none. */
async function loadSession(drafts: unknown[] = []): Promise<EditorSession> {
  fetchMock.mockResolvedValueOnce(json({ etag: "e1", doc }));
  const mine = (drafts as Array<{ workId?: string }>).find((d) => d.workId === "w1");
  fetchMock.mockResolvedValueOnce(mine ? json(mine) : json({ error: "no such draft" }, 404));
  session = createEditorSession("w1");
  await session.load();
  fetchMock.mockClear();
  return session;
}

beforeEach(() => {
  setConfig({ apiBase: "", localAuth: true, provider: "test" });
  localStorage.clear();
  invalidateAccess();
  fetchMock = vi.fn();
  vi.stubGlobal("fetch", fetchMock);
});

afterEach(() => {
  session?.destroy();
  session = null;
  vi.unstubAllGlobals();
  vi.useRealTimers();
  setConfig(null);
});

describe("412 conflict flow", () => {
  it("save hits a 412, reload rebase-previews, and the retry lands", async () => {
    await seedSession();
    const s = await loadSession();
    expect(get(s).etag).toBe("e1");

    s.stage(op);
    expect(get(s).ops).toHaveLength(1);

    // Save against a stale etag: the server answers 412 with fresh state.
    fetchMock.mockResolvedValueOnce(json({ workId: "w1", etag: "e2", nquads: "<a> <b> <c> ." }, 412));
    expect(await s.save()).toBe(false);
    expect(get(s).conflict).toBe(true);
    expect(get(s).ops).toHaveLength(1); // staged ops survive the conflict
    const saveInit = fetchMock.mock.calls[0][1];
    expect(saveInit.headers["If-Match"]).toBe("e1");
    fetchMock.mockClear();

    // Reload: fresh doc, then the staged ops replay as a dry run.
    fetchMock.mockResolvedValueOnce(json({ etag: "e2", doc }));
    fetchMock.mockResolvedValueOnce(json({ etag: "e2", diff: { added: ["+t"], removed: [] } }));
    await s.reload();
    expect(get(s).conflict).toBe(false);
    expect(get(s).etag).toBe("e2");
    expect(get(s).diff).toEqual({ added: ["+t"], removed: [] });
    expect(JSON.parse(fetchMock.mock.calls[1][1].body)).toEqual({ ops: [op], dryRun: true });
    fetchMock.mockClear();

    // The retried save carries the fresh etag and succeeds.
    fetchMock.mockResolvedValueOnce(json({ workId: "w1", etag: "e3", diff: { added: ["+t"], removed: [] } }));
    fetchMock.mockResolvedValueOnce(json({ etag: "e3", doc }));
    expect(await s.save()).toBe(true);
    expect(fetchMock.mock.calls[0][1].headers["If-Match"]).toBe("e2");
    expect(get(s).etag).toBe("e3");
    expect(get(s).ops).toEqual([]);
    expect(get(s).notice).toContain("Saved 1 edit");
  });

  it("ops that no longer apply surface the server's validation message on replay", async () => {
    await seedSession();
    const s = await loadSession();
    s.stage(op);
    fetchMock.mockResolvedValueOnce(json({ workId: "w1", etag: "e2", nquads: "" }, 412));
    await s.save();
    fetchMock.mockClear();
    fetchMock.mockResolvedValueOnce(json({ etag: "e2", doc }));
    fetchMock.mockResolvedValueOnce(json({ error: "editor: op 0 (add work.tags): value already present" }, 400));
    await s.reload();
    expect(get(s).conflict).toBe(false);
    expect(get(s).diff).toBeNull();
    expect(get(s).opError).toBe("editor: op 0 (add work.tags): value already present");
  });
});

describe("draft autosave", () => {
  it("creates a draft 3s after the first edit and updates it after the next", async () => {
    vi.useFakeTimers();
    await seedSession();
    const s = await loadSession();

    s.stage(op);
    fetchMock.mockResolvedValueOnce(json({ id: "d1", workId: "w1", body: {}, updatedAt: "x" }, 201));
    await vi.advanceTimersByTimeAsync(AUTOSAVE_MS);
    let [url, init] = fetchMock.mock.calls[0];
    expect(url).toBe("/v1/drafts");
    expect(init.method).toBe("POST");
    expect(JSON.parse(init.body)).toEqual({ workId: "w1", body: { baseEtag: "e1", ops: [op] } });
    expect(get(s).draftId).toBe("d1");
    fetchMock.mockClear();

    s.stage({ resource: "work", path: "tags", action: "add", value: { v: "ships" } });
    fetchMock.mockResolvedValueOnce(json({ id: "d1", workId: "w1", body: {}, updatedAt: "y" }));
    await vi.advanceTimersByTimeAsync(AUTOSAVE_MS);
    [url, init] = fetchMock.mock.calls[0];
    expect(url).toBe("/v1/drafts/d1");
    expect(init.method).toBe("PUT");
    expect(JSON.parse(init.body).body.ops).toHaveLength(2);
  });

  it("editing past the resume offer adopts the pending draft slot", async () => {
    vi.useFakeTimers();
    await seedSession();
    const pending = { id: "d9", workId: "w1", body: { baseEtag: "e0", ops: [op] }, updatedAt: "x" };
    const s = await loadSession([pending]);
    s.stage({ resource: "work", path: "tags", action: "add", value: { v: "fresh" } });
    fetchMock.mockResolvedValueOnce(json({ id: "d9", workId: "w1", body: {}, updatedAt: "y" }));
    await vi.advanceTimersByTimeAsync(AUTOSAVE_MS);
    const [url, init] = fetchMock.mock.calls[0];
    expect(url).toBe("/v1/drafts/d9");
    expect(init.method).toBe("PUT");
    expect(get(s).draftId).toBe("d9");
    expect(get(s).pendingDraft).toBeNull();
  });

  it("undoing every edit deletes the autosaved draft", async () => {
    vi.useFakeTimers();
    await seedSession();
    const s = await loadSession();
    s.stage(op);
    fetchMock.mockResolvedValueOnce(json({ id: "d1", workId: "w1", body: {}, updatedAt: "x" }, 201));
    await vi.advanceTimersByTimeAsync(AUTOSAVE_MS);
    fetchMock.mockClear();
    s.unstage(op);
    fetchMock.mockResolvedValueOnce(new Response(null, { status: 204 }));
    await vi.advanceTimersByTimeAsync(AUTOSAVE_MS);
    const [url, init] = fetchMock.mock.calls[0];
    expect(url).toBe("/v1/drafts/d1");
    expect(init.method).toBe("DELETE");
    expect(get(s).draftId).toBe("");
  });
});

describe("draft resume", () => {
  const pending = { id: "d9", workId: "w1", body: { baseEtag: "e0", ops: [op] }, updatedAt: "2026-07-01T00:00:00Z" };

  it("offers a matching draft on open and resume loads its ops", async () => {
    await seedSession();
    const s = await loadSession([{ id: "dx", workId: "other" }, pending]);
    expect(get(s).pendingDraft?.id).toBe("d9");
    s.resumeDraft();
    const st = get(s);
    expect(st.pendingDraft).toBeNull();
    expect(st.draftId).toBe("d9");
    expect(st.ops).toEqual([op]);
    expect(st.notice).toContain("predates"); // baseEtag e0 != loaded e1
    expect(fetchMock).not.toHaveBeenCalled(); // resume itself is local
  });

  it("discarding the offered draft deletes it", async () => {
    await seedSession();
    const s = await loadSession([pending]);
    fetchMock.mockResolvedValueOnce(new Response(null, { status: 204 }));
    await s.discardDraft();
    expect(get(s).pendingDraft).toBeNull();
    expect(fetchMock.mock.calls[0][0]).toBe("/v1/drafts/d9");
    expect(fetchMock.mock.calls[0][1].method).toBe("DELETE");
  });

  it("a successful save deletes the resumed draft", async () => {
    await seedSession();
    const s = await loadSession([pending]);
    s.resumeDraft();
    fetchMock.mockResolvedValueOnce(json({ workId: "w1", etag: "e2", diff: { added: [], removed: [] } }));
    fetchMock.mockResolvedValueOnce(new Response(null, { status: 204 }));
    fetchMock.mockResolvedValueOnce(json({ etag: "e2", doc }));
    expect(await s.save()).toBe(true);
    const urls = fetchMock.mock.calls.map((c) => c[0]);
    expect(urls).toEqual(["/v1/works/w1/ops", "/v1/drafts/d9", "/v1/works/w1/doc"]);
    expect(fetchMock.mock.calls[1][1].method).toBe("DELETE");
    expect(get(s).draftId).toBe("");
  });
});

describe("preview and discard", () => {
  it("preview dry-runs the staged ops and edits clear the stale diff", async () => {
    await seedSession();
    const s = await loadSession();
    s.stage(op);
    fetchMock.mockResolvedValueOnce(json({ etag: "e1", diff: { added: ["+t"], removed: ["-t"] } }));
    await s.preview();
    expect(get(s).diff).toEqual({ added: ["+t"], removed: ["-t"] });
    expect(JSON.parse(fetchMock.mock.calls[0][1].body).dryRun).toBe(true);
    s.stage({ resource: "work", path: "tags", action: "add", value: { v: "more" } });
    expect(get(s).diff).toBeNull();
  });

  it("discard drops staged ops and the conflict flag without a draft call", async () => {
    await seedSession();
    const s = await loadSession();
    s.stage(op);
    fetchMock.mockResolvedValueOnce(json({ workId: "w1", etag: "e2", nquads: "" }, 412));
    await s.save();
    fetchMock.mockClear();
    await s.discard();
    const st = get(s);
    expect(st.ops).toEqual([]);
    expect(st.conflict).toBe(false);
    expect(fetchMock).not.toHaveBeenCalled(); // no draft ever autosaved
  });
});
