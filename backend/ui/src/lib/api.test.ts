// Request shaping and the 401-retry-through-refresh path, with fetch fully
// injected. A local login seeds the auth module's in-memory token.
import { afterEach, beforeEach, describe, expect, it, vi, type Mock } from "vitest";
import { fetchQueue, fetchWorkDoc, fetchWorks, searchTerms, ApiError } from "./api";
import { invalidateAccess, loginLocal } from "./auth";
import { setConfig } from "./config";

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
