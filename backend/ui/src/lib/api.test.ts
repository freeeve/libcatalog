// Request shaping and the 401-retry-through-refresh path, with fetch fully
// injected. A local login seeds the auth module's in-memory token.
import { afterEach, beforeEach, describe, expect, it, vi, type Mock } from "vitest";
import {
  clearTermCache,
  createDraft,
  decidePromotion,
  deleteDraft,
  fetchAudit,
  fetchDraft,
  fetchDrafts,
  fetchPromotions,
  fetchQueue,
  fetchTags,
  fetchTerm,
  fetchWorkDoc,
  fetchWorks,
  postOps,
  postPublish,
  postReview,
  proposePromotion,
  resolveTerm,
  searchFolkTerms,
  searchTerms,
  setFolkTermStatus,
  updateDraft,
  ApiError,
  ConflictError,
  humanApiMessage,
} from "./api";
import { invalidateAccess, loginLocal } from "./auth";
import { setConfig } from "./config";
import type { Decision, Op } from "./types";

function jwtLike(tag: string): string {
  const body = btoa(JSON.stringify({ email: "a@b.co", roles: ["librarian"], tag }))
    .replace(/\+/g, "-")
    .replace(/\//g, "_")
    .replace(/=+$/, "");
  return `hdr.${body}.sig`;
}

const tokenA = jwtLike("a");
const tokenB = jwtLike("b");

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status });
}

let fetchMock: Mock;

async function seedSession(expiresIn = 900): Promise<void> {
  fetchMock.mockResolvedValueOnce(json({ accessToken: tokenA, refreshToken: "r1", expiresIn }));
  await loginLocal("a@b.co", "pw");
  fetchMock.mockClear();
}

beforeEach(() => {
  setConfig({ apiBase: "", localAuth: true, provider: "test" });
  localStorage.clear();
  invalidateAccess();
  clearTermCache();
  fetchMock = vi.fn();
  vi.stubGlobal("fetch", fetchMock);
});

afterEach(() => {
  vi.unstubAllGlobals();
  setConfig(null);
});

describe("request shaping", () => {
  it("fetchWorks encodes query and limit and sends the bearer", async () => {
    await seedSession();
    fetchMock.mockResolvedValueOnce(json({ works: [], total: 0 }));
    await fetchWorks("sea monsters", 10);
    const [url, init] = fetchMock.mock.calls[0];
    expect(url).toBe("/v1/works?q=sea+monsters&limit=10");
    expect(init.method).toBe("GET");
    expect(init.headers.Authorization).toBe(`Bearer ${tokenA}`);
    expect(init.body).toBeUndefined();
  });

  it("fetchWorkDoc escapes the work id", async () => {
    await seedSession();
    fetchMock.mockResolvedValueOnce(json({ etag: "e", doc: {} }));
    await fetchWorkDoc("w/1");
    expect(fetchMock.mock.calls[0][0]).toBe("/v1/works/w%2F1/doc");
  });

  it("fetchQueue defaults to PENDING and carries cursor", async () => {
    await seedSession();
    fetchMock.mockResolvedValueOnce(json({ items: [] }));
    await fetchQueue({ cursor: "c1", limit: 25 });
    expect(fetchMock.mock.calls[0][0]).toBe("/v1/queue?status=PENDING&cursor=c1&limit=25");
  });

  it("searchTerms carries scheme and query", async () => {
    await seedSession();
    fetchMock.mockResolvedValueOnce(json({ terms: [] }));
    await searchTerms("lcsh", "sea");
    expect(fetchMock.mock.calls[0][0]).toBe("/v1/terms?scheme=lcsh&q=sea");
  });

  it("prefixes a non-empty apiBase", async () => {
    setConfig({ apiBase: "https://api.example.org", localAuth: true, provider: "test" });
    await seedSession();
    fetchMock.mockResolvedValueOnce(json({ works: [], total: 0 }));
    await fetchWorks("x");
    expect(fetchMock.mock.calls[0][0]).toBe("https://api.example.org/v1/works?q=x&limit=50");
  });
});

describe("queue and review wrappers", () => {
  const decision: Decision = {
    workId: "w1",
    term: { scheme: "lcsh", id: "http://id.loc.gov/sh1", label: "Sea monsters" },
    type: "ADD",
    approve: true,
  };

  it("fetchQueue carries every filter", async () => {
    await seedSession();
    fetchMock.mockResolvedValueOnce(json({ items: [] }));
    await fetchQueue({ status: "APPROVED", scheme: "lcsh", provenance: "PIPELINE", type: "ADD", cursor: "c2" });
    expect(fetchMock.mock.calls[0][0]).toBe("/v1/queue?status=APPROVED&scheme=lcsh&provenance=PIPELINE&type=ADD&cursor=c2");
  });

  it("postReview ships the decision batch with the publish flag", async () => {
    await seedSession();
    fetchMock.mockResolvedValueOnce(json({ reviewed: 1, published: 1, skipped: 0 }));
    const res = await postReview([decision], true);
    const [url, init] = fetchMock.mock.calls[0];
    expect(url).toBe("/v1/review");
    expect(init.method).toBe("POST");
    expect(init.headers["Content-Type"]).toBe("application/json");
    expect(JSON.parse(init.body)).toEqual({ decisions: [decision], publish: true });
    expect(res.reviewed).toBe(1);
  });

  it("postPublish POSTs with no body", async () => {
    await seedSession();
    fetchMock.mockResolvedValueOnce(json({ published: 2, skipped: 1 }));
    const res = await postPublish();
    const [url, init] = fetchMock.mock.calls[0];
    expect(url).toBe("/v1/publish");
    expect(init.method).toBe("POST");
    expect(init.body).toBeUndefined();
    expect(res.published).toBe(2);
  });

  it("setFolkTermStatus shapes the governance action and accepts 204", async () => {
    await seedSession();
    fetchMock.mockResolvedValueOnce(new Response(null, { status: 204 }));
    await setFolkTermStatus("blockFolk", "cozy-fantasy");
    const [url, init] = fetchMock.mock.calls[0];
    expect(url).toBe("/v1/terms");
    expect(JSON.parse(init.body)).toEqual({ action: "blockFolk", folkTerm: "cozy-fantasy" });
  });
});

describe("term and tag wrappers", () => {
  it("fetchTerm encodes the term URI", async () => {
    await seedSession();
    fetchMock.mockResolvedValueOnce(json({ scheme: "lcsh", id: "http://id.loc.gov/sh1", labels: { en: "Sea" } }));
    await fetchTerm("lcsh", "http://id.loc.gov/sh1");
    expect(fetchMock.mock.calls[0][0]).toBe("/v1/term?scheme=lcsh&id=http%3A%2F%2Fid.loc.gov%2Fsh1");
  });

  it("resolveTerm caches by scheme and id", async () => {
    await seedSession();
    // Fresh Response per call: a Response body is single-read.
    fetchMock.mockImplementation(() => Promise.resolve(json({ scheme: "lcsh", id: "u1", labels: { en: "One" } })));
    const a = await resolveTerm("lcsh", "u1");
    const b = await resolveTerm("lcsh", "u1");
    expect(a).toBe(b);
    expect(fetchMock).toHaveBeenCalledTimes(1);
    await resolveTerm("fast", "u1"); // different scheme misses the cache
    expect(fetchMock).toHaveBeenCalledTimes(2);
  });

  it("resolveTerm does not cache failures", async () => {
    await seedSession();
    fetchMock.mockResolvedValueOnce(json({ error: "unknown term" }, 404));
    await expect(resolveTerm("lcsh", "missing")).rejects.toMatchObject({ status: 404 });
    fetchMock.mockResolvedValueOnce(json({ scheme: "lcsh", id: "missing", labels: {} }));
    await expect(resolveTerm("lcsh", "missing")).resolves.toMatchObject({ id: "missing" });
  });

  it("searchFolkTerms pins scheme=folk", async () => {
    await seedSession();
    fetchMock.mockResolvedValueOnce(json({ terms: [] }));
    await searchFolkTerms("cozy");
    expect(fetchMock.mock.calls[0][0]).toBe("/v1/terms?scheme=folk&q=cozy");
  });

  it("fetchTags carries the query", async () => {
    await seedSession();
    fetchMock.mockResolvedValueOnce(json({ tags: [{ tag: "sea", count: 3 }] }));
    const res = await fetchTags("sea");
    expect(fetchMock.mock.calls[0][0]).toBe("/v1/tags?q=sea");
    expect(res.tags[0].count).toBe(3);
  });
});

describe("promotion wrappers", () => {
  it("fetchPromotions with and without a status filter", async () => {
    await seedSession();
    fetchMock.mockResolvedValueOnce(json({ promotions: [] }));
    await fetchPromotions();
    expect(fetchMock.mock.calls[0][0]).toBe("/v1/promotions");
    fetchMock.mockResolvedValueOnce(json({ promotions: [] }));
    await fetchPromotions("PENDING");
    expect(fetchMock.mock.calls[1][0]).toBe("/v1/promotions?status=PENDING");
  });

  it("proposePromotion shapes the body and surfaces 409", async () => {
    await seedSession();
    fetchMock.mockResolvedValueOnce(json({ tag: "cozy", status: "PENDING" }, 201));
    await proposePromotion("cozy", { scheme: "lcsh", id: "http://id.loc.gov/sh9" });
    const [url, init] = fetchMock.mock.calls[0];
    expect(url).toBe("/v1/promotions");
    expect(JSON.parse(init.body)).toEqual({ tag: "cozy", term: { scheme: "lcsh", id: "http://id.loc.gov/sh9" } });
    fetchMock.mockResolvedValueOnce(json({ error: "promotion already proposed" }, 409));
    await expect(proposePromotion("cozy", { scheme: "lcsh", id: "x" })).rejects.toMatchObject({ status: 409 });
  });

  it("decidePromotion shapes the body and returns the works count", async () => {
    await seedSession();
    fetchMock.mockResolvedValueOnce(json({ promotion: { tag: "cozy" }, works: 12 }));
    const res = await decidePromotion("cozy", true);
    const [url, init] = fetchMock.mock.calls[0];
    expect(url).toBe("/v1/promotions/decide");
    expect(JSON.parse(init.body)).toEqual({ tag: "cozy", approve: true });
    expect(res.works).toBe(12);
  });
});

describe("ops wrapper", () => {
  const ops: Op[] = [{ resource: "work", path: "tags", action: "add", value: { v: "sea" } }];

  it("postOps sends If-Match and the op batch", async () => {
    await seedSession();
    fetchMock.mockResolvedValueOnce(json({ workId: "w1", etag: "e2", diff: { added: ["a"], removed: [] } }));
    const res = await postOps("w1", ops, { ifMatch: "e1" });
    const [url, init] = fetchMock.mock.calls[0];
    expect(url).toBe("/v1/works/w1/ops");
    expect(init.method).toBe("POST");
    expect(init.headers["If-Match"]).toBe("e1");
    expect(JSON.parse(init.body)).toEqual({ ops });
    expect(res.etag).toBe("e2");
    expect(res.diff.added).toEqual(["a"]);
  });

  it("postOps dryRun omits If-Match and carries the flag", async () => {
    await seedSession();
    fetchMock.mockResolvedValueOnce(json({ etag: "e1", diff: { added: [], removed: ["r"] } }));
    const res = await postOps("w/1", ops, { dryRun: true });
    const [url, init] = fetchMock.mock.calls[0];
    expect(url).toBe("/v1/works/w%2F1/ops");
    expect(init.headers["If-Match"]).toBeUndefined();
    expect(JSON.parse(init.body)).toEqual({ ops, dryRun: true });
    expect(res.diff.removed).toEqual(["r"]);
  });

  it("postOps surfaces a 412 as ConflictError with the fresh state", async () => {
    await seedSession();
    fetchMock.mockResolvedValueOnce(json({ workId: "w1", etag: "e9", nquads: "<a> <b> <c> ." }, 412));
    const err = await postOps("w1", ops, { ifMatch: "stale" }).catch((e: unknown) => e);
    expect(err).toBeInstanceOf(ConflictError);
    expect((err as ConflictError).status).toBe(412);
    expect((err as ConflictError).state).toEqual({ workId: "w1", etag: "e9", nquads: "<a> <b> <c> ." });
  });

  it("postOps surfaces validation failures with the server's message", async () => {
    await seedSession();
    fetchMock.mockResolvedValueOnce(json({ error: 'editor: op 0 (add work.nope): unknown field "nope"' }, 400));
    await expect(postOps("w1", ops, { dryRun: true })).rejects.toMatchObject({
      status: 400,
      message: 'editor: op 0 (add work.nope): unknown field "nope"',
    });
  });
});

describe("draft wrappers", () => {
  const body = { baseEtag: "e1", ops: [{ resource: "work", path: "tags", action: "add", value: { v: "sea" } } as Op] };

  it("createDraft POSTs workId and the opaque body", async () => {
    await seedSession();
    fetchMock.mockResolvedValueOnce(json({ id: "d1", workId: "w1", body, updatedAt: "2026-07-01T00:00:00Z" }, 201));
    const d = await createDraft("w1", body);
    const [url, init] = fetchMock.mock.calls[0];
    expect(url).toBe("/v1/drafts");
    expect(init.method).toBe("POST");
    expect(JSON.parse(init.body)).toEqual({ workId: "w1", body });
    expect(d.id).toBe("d1");
  });

  it("fetchDrafts lists and fetchDraft reads one", async () => {
    await seedSession();
    fetchMock.mockResolvedValueOnce(json({ drafts: [{ id: "d1", workId: "w1", body, updatedAt: "x" }] }));
    const list = await fetchDrafts();
    expect(fetchMock.mock.calls[0][0]).toBe("/v1/drafts");
    expect(list.drafts).toHaveLength(1);
    fetchMock.mockResolvedValueOnce(json({ id: "d1", workId: "w1", body, updatedAt: "x" }));
    const d = await fetchDraft("d1");
    expect(fetchMock.mock.calls[1][0]).toBe("/v1/drafts/d1");
    expect(d.body.baseEtag).toBe("e1");
  });

  it("updateDraft PUTs to the draft id", async () => {
    await seedSession();
    fetchMock.mockResolvedValueOnce(json({ id: "d1", workId: "w1", body, updatedAt: "y" }));
    await updateDraft("d1", "w1", body);
    const [url, init] = fetchMock.mock.calls[0];
    expect(url).toBe("/v1/drafts/d1");
    expect(init.method).toBe("PUT");
    expect(JSON.parse(init.body)).toEqual({ workId: "w1", body });
  });

  it("deleteDraft accepts the 204", async () => {
    await seedSession();
    fetchMock.mockResolvedValueOnce(new Response(null, { status: 204 }));
    await expect(deleteDraft("d1")).resolves.toBeUndefined();
    const [url, init] = fetchMock.mock.calls[0];
    expect(url).toBe("/v1/drafts/d1");
    expect(init.method).toBe("DELETE");
  });
});

describe("audit wrapper", () => {
  it("fetchAudit shapes month and the optional workId", async () => {
    await seedSession();
    fetchMock.mockResolvedValueOnce(json({ month: "2026-07", entries: [] }));
    await fetchAudit("2026-07");
    expect(fetchMock.mock.calls[0][0]).toBe("/v1/audit?month=2026-07");
    fetchMock.mockResolvedValueOnce(json({ month: "2026-07", entries: [] }));
    const res = await fetchAudit("2026-07", "w/1");
    expect(fetchMock.mock.calls[1][0]).toBe("/v1/audit?month=2026-07&workId=w%2F1");
    expect(res.entries).toEqual([]);
  });
});

describe("401 retry", () => {
  it("refreshes once and retries with the new token", async () => {
    await seedSession();
    fetchMock
      .mockResolvedValueOnce(json({ error: "expired" }, 401)) // first works call
      .mockResolvedValueOnce(json({ accessToken: tokenB, refreshToken: "r2", expiresIn: 900 })) // refresh
      .mockResolvedValueOnce(json({ works: [], total: 3 })); // retry
    const page = await fetchWorks("q");
    expect(page.total).toBe(3);
    expect(fetchMock).toHaveBeenCalledTimes(3);
    expect(fetchMock.mock.calls[1][0]).toBe("/v1/auth/refresh");
    expect(fetchMock.mock.calls[2][1].headers.Authorization).toBe(`Bearer ${tokenB}`);
    expect(localStorage.getItem("lcat-refresh")).toBe("r2");
  });

  it("a second 401 surfaces as ApiError, not a loop", async () => {
    await seedSession();
    fetchMock
      .mockResolvedValueOnce(json({}, 401))
      .mockResolvedValueOnce(json({ accessToken: tokenB, refreshToken: "r2", expiresIn: 900 }))
      .mockResolvedValueOnce(json({}, 401));
    await expect(fetchWorks("q")).rejects.toThrowError(ApiError);
    expect(fetchMock).toHaveBeenCalledTimes(3);
  });

  it("no session at all fails fast without network", async () => {
    await expect(fetchWorks("q")).rejects.toMatchObject({ status: 401 });
    expect(fetchMock).not.toHaveBeenCalled();
  });
});

// service-internal error prefixes must not reach a cataloger's error
// banner. humanApiMessage strips the "pkg:" and its "invalid request:/invalid
// source:" tail; every error-surfacing screen routes through it.
describe("humanApiMessage", () => {
  it("strips the package prefix and capitalizes", () => {
    expect(humanApiMessage(new ApiError(400, "batch: invalid request: title is required"), "fallback")).toBe(
      "Title is required",
    );
  });

  it("strips 'invalid source:' too, not just 'invalid request:'", () => {
    expect(humanApiMessage(new ApiError(400, "vocabsrc: invalid source: urls must be http(s)"), "fallback")).toBe(
      "Urls must be http(s)",
    );
  });

  it("strips a bare package prefix with no invalid-* tail", () => {
    expect(humanApiMessage(new ApiError(409, "copycat: batch already committed"), "fallback")).toBe(
      "Batch already committed",
    );
  });

  it("falls back for a non-ApiError, and for a message that strips to empty", () => {
    expect(humanApiMessage(new Error("boom"), "loading failed")).toBe("loading failed");
    expect(humanApiMessage(new ApiError(400, "copycat: invalid request: "), "quick-add failed")).toBe(
      "quick-add failed",
    );
  });
});

// the anti-drift guard. A screen that assigns the raw ApiError
// message straight to its error banner re-leaks the package prefix (the exact
// regression 337 fixed on nine screens). No screen source may pair
// `instanceof ApiError` with a bare `e.message` on the same line -- the humanizer
// must sit between them. New screens inherit the rule for free.
describe("screens never surface a raw ApiError message", () => {
  const sources = import.meta.glob("/src/screens/*.svelte", {
    eager: true,
    query: "?raw",
    import: "default",
  }) as Record<string, string>;

  for (const [path, src] of Object.entries(sources)) {
    it(`${path.split("/").pop()} humanizes its error copy`, () => {
      const offenders = src
        .split("\n")
        .filter((line) => /instanceof ApiError/.test(line) && /\be\.message\b/.test(line));
      expect(offenders, offenders.join("\n")).toHaveLength(0);
    });
  }
});
