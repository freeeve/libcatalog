// Local-auth session lifecycle against an injected fetch: login adopts
// tokens, refresh is single-flight and rotates the stored refresh token, a
// definitive refresh failure clears the session. No network.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { expireSession, getToken, handleOidcCallback, invalidateAccess, loginLocal, onSessionExpired, session, startOidcLogin } from "./auth";
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

// the shell learns the session died through onSessionExpired --
// on a terminal refresh failure or a sibling tab's sign-out, never on a
// fresh visit that simply has no session.
describe("session expiry notification", () => {
  it("notifies once on a terminal refresh failure", async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(tokenResponse(staffJwt, "r1", 30))
      .mockResolvedValueOnce(new Response("{}", { status: 401 }));
    vi.stubGlobal("fetch", fetchMock);
    const expired = vi.fn();
    const off = onSessionExpired(expired);

    await loginLocal("a@b.co", "pw");
    await expect(getToken()).resolves.toBe("");
    expect(expired).toHaveBeenCalledTimes(1);
    // Declaring an already-cleared session dead again is silent.
    expireSession();
    expect(expired).toHaveBeenCalledTimes(1);
    off();
  });

  it("notifies when a sibling tab removed the refresh token", async () => {
    const fetchMock = vi.fn().mockResolvedValueOnce(tokenResponse(staffJwt, "r1", 30));
    vi.stubGlobal("fetch", fetchMock);
    const expired = vi.fn();
    const off = onSessionExpired(expired);

    await loginLocal("a@b.co", "pw");
    localStorage.removeItem(REFRESH_KEY); // the other tab signed out
    await expect(getToken()).resolves.toBe("");
    expect(expired).toHaveBeenCalledTimes(1);
    off();
  });

  it("stays silent for a fresh visitor with no session", async () => {
    const expired = vi.fn();
    const off = onSessionExpired(expired);
    await expect(getToken()).resolves.toBe("");
    expect(expired).not.toHaveBeenCalled();
    off();
  });
});

describe("oidc redirect_uri", () => {
  const STATE_KEY = "lcat-state";
  const VERIFIER_KEY = "lcat-pkce";

  beforeEach(() => {
    setConfig({ apiBase: "", localAuth: false, provider: "test", oidc: { issuer: "https://issuer.example", clientId: "cid" } });
    sessionStorage.clear();
    history.replaceState(null, "", "/");
  });

  afterEach(() => {
    history.replaceState(null, "", "/");
  });

  it("sends a fragment-free redirect_uri to /authorize", async () => {
    const assign = vi.fn();
    vi.stubGlobal("location", { origin: window.location.origin, assign });

    await startOidcLogin();

    expect(assign).toHaveBeenCalledOnce();
    const url = new URL(assign.mock.calls[0][0] as string);
    expect(url.origin + url.pathname).toBe("https://issuer.example/authorize");
    const redirect = url.searchParams.get("redirect_uri");
    expect(redirect).toBe(location.origin + "/_auth/callback");
    expect(redirect).not.toContain("#");
  });

  it("completes a real-path callback and cleans the URL to the hash root", async () => {
    sessionStorage.setItem(STATE_KEY, "st8");
    sessionStorage.setItem(VERIFIER_KEY, "vfy");
    history.replaceState(null, "", "/_auth/callback?code=abc&state=st8");
    const fetchMock = vi.fn().mockResolvedValue(tokenResponse(staffJwt, "r1", 900));
    vi.stubGlobal("fetch", fetchMock);

    await expect(handleOidcCallback()).resolves.toBe(true);

    expect(fetchMock).toHaveBeenCalledWith("/v1/auth/exchange", expect.objectContaining({ method: "POST" }));
    const grant = new URLSearchParams(fetchMock.mock.calls[0][1].body as string);
    expect(grant.get("code")).toBe("abc");
    expect(grant.get("code_verifier")).toBe("vfy");
    expect(grant.get("redirect_uri")).toBe(location.origin + "/_auth/callback");
    expect(location.pathname).toBe("/");
    expect(location.hash).toBe("#/");
    expect(session()?.roles).toEqual(["librarian"]);
  });
});
