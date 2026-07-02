// Local-auth session lifecycle against an injected fetch: login adopts
// tokens, refresh is single-flight and rotates the stored refresh token, a
// definitive refresh failure clears the session. No network.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { getToken, invalidateAccess, loginLocal, session } from "./auth";
import { setConfig } from "./config";

const REFRESH_KEY = "lcat-refresh";

function jwt(payload: Record<string, unknown>): string {
  const body = btoa(JSON.stringify(payload)).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
  return `hdr.${body}.sig`;
}

function tokenResponse(access: string, refresh: string, expiresIn: number): Response {
  return new Response(JSON.stringify({ accessToken: access, refreshToken: refresh, expiresIn }), { status: 200 });
}

const staffJwt = jwt({ email: "a@b.co", roles: ["librarian"] });

beforeEach(() => {
  setConfig({ apiBase: "", localAuth: true, provider: "test" });
  localStorage.clear();
  invalidateAccess();
});

afterEach(() => {
  vi.unstubAllGlobals();
  setConfig(null);
});

describe("local login", () => {
  it("adopts tokens and exposes JWT claims", async () => {
    const fetchMock = vi.fn().mockResolvedValue(tokenResponse(staffJwt, "r1", 900));
    vi.stubGlobal("fetch", fetchMock);

    const s = await loginLocal("a@b.co", "pw");
    expect(fetchMock).toHaveBeenCalledWith(
      "/v1/auth/login",
      expect.objectContaining({ method: "POST", body: JSON.stringify({ email: "a@b.co", password: "pw" }) }),
    );
    expect(s.email).toBe("a@b.co");
    expect(s.roles).toEqual(["librarian"]);
    expect(localStorage.getItem(REFRESH_KEY)).toBe("r1");
    await expect(getToken()).resolves.toBe(staffJwt); // fresh -- no refresh call
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it("reads a single 'role' string claim", async () => {
    const solo = jwt({ email: "m@b.co", role: "moderator" });
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(tokenResponse(solo, "r1", 900)));
    await loginLocal("m@b.co", "pw");
    expect(session()?.roles).toEqual(["moderator"]);
  });
});

describe("refresh", () => {
  it("rotates the stored refresh token and is single-flight", async () => {
    // expiresIn 30 is inside the 60s renewal window, so getToken refreshes.
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(tokenResponse(staffJwt, "r1", 30))
      .mockResolvedValueOnce(tokenResponse(staffJwt, "r2", 900));
    vi.stubGlobal("fetch", fetchMock);

    await loginLocal("a@b.co", "pw");
    const [t1, t2] = await Promise.all([getToken(), getToken()]);
    expect(t1).toBe(staffJwt);
    expect(t2).toBe(staffJwt);
    expect(localStorage.getItem(REFRESH_KEY)).toBe("r2");
    // Exactly one refresh for the two concurrent calls: login + refresh = 2.
    expect(fetchMock).toHaveBeenCalledTimes(2);
    expect(fetchMock.mock.calls[1][0]).toBe("/v1/auth/refresh");
    expect(fetchMock.mock.calls[1][1].body).toBe(JSON.stringify({ refreshToken: "r1" }));
  });

  it("a 401 refresh clears the session", async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(tokenResponse(staffJwt, "r1", 30))
      .mockResolvedValueOnce(new Response("{}", { status: 401 }));
    vi.stubGlobal("fetch", fetchMock);

    await loginLocal("a@b.co", "pw");
    await expect(getToken()).resolves.toBe("");
    expect(localStorage.getItem(REFRESH_KEY)).toBeNull();
    expect(session()).toBeNull();
  });

  it("a 5xx refresh keeps the refresh token for a later attempt", async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(tokenResponse(staffJwt, "r1", 30))
      .mockResolvedValueOnce(new Response("{}", { status: 503 }));
    vi.stubGlobal("fetch", fetchMock);

    await loginLocal("a@b.co", "pw");
    await expect(getToken()).resolves.toBe("");
    expect(localStorage.getItem(REFRESH_KEY)).toBe("r1");
  });
});
