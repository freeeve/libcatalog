// Session management for both auth modes the backend can serve: built-in
// local users (POST /v1/auth/login|refresh|logout) and OIDC
// authorization-code + PKCE against an external issuer, exchanged through the
// API's /v1/auth/exchange proxy (which holds the confidential client secret).
// The access token lives in memory; the rotating refresh token sits in
// localStorage so the staff session survives reloads and new tabs.
import { apiBase, getConfig } from "./config";

const VERIFIER_KEY = "lcat-pkce";
const STATE_KEY = "lcat-state";
const REFRESH_KEY = "lcat-refresh";
const MODE_KEY = "lcat-auth-mode";
const LOCK_NAME = "lcat-refresh";

type Mode = "local" | "oidc";

let accessToken = "";
let expiresAt = 0;

export interface Session {
  email: string;
  roles: string[];
}

function b64url(bytes: Uint8Array): string {
  return btoa(String.fromCharCode(...bytes)).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
}

function randomString(): string {
  const bytes = new Uint8Array(32);
  crypto.getRandomValues(bytes);
  return b64url(bytes);
}

function redirectUri(): string {
  return location.origin + "/#/callback";
}

interface TokenPayload {
  accessToken: string;
  refreshToken: string;
  expiresIn: number;
}

/** Normalises local (camelCase) and issuer (snake_case) token responses. */
function normalize(raw: Record<string, unknown>): TokenPayload {
  return {
    accessToken: String(raw.accessToken ?? raw.access_token ?? ""),
    refreshToken: String(raw.refreshToken ?? raw.refresh_token ?? ""),
    expiresIn: Number(raw.expiresIn ?? raw.expires_in ?? 900),
  };
}

function adopt(raw: Record<string, unknown>, mode: Mode): void {
  const tok = normalize(raw);
  accessToken = tok.accessToken;
  expiresAt = Date.now() + tok.expiresIn * 1000;
  localStorage.setItem(MODE_KEY, mode);
  if (tok.refreshToken) localStorage.setItem(REFRESH_KEY, tok.refreshToken);
}

/** Local email/password login. Throws on bad credentials. */
export async function loginLocal(email: string, password: string): Promise<Session> {
  const res = await fetch(`${apiBase()}/v1/auth/login`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ email, password }),
  });
  if (!res.ok) {
    const detail = await res.json().catch(() => ({}) as { error?: string });
    throw new Error(detail.error || `login failed (${res.status})`);
  }
  adopt(await res.json(), "local");
  return session() ?? { email, roles: [] };
}

/** Sends the browser to the OIDC issuer's hosted login (PKCE). */
export async function startOidcLogin(): Promise<void> {
  const oidc = getConfig().oidc;
  if (!oidc) throw new Error("OIDC is not configured");
  const verifier = randomString();
  const state = randomString();
  sessionStorage.setItem(VERIFIER_KEY, verifier);
  sessionStorage.setItem(STATE_KEY, state);
  const digest = await crypto.subtle.digest("SHA-256", new TextEncoder().encode(verifier));
  const params = new URLSearchParams({
    client_id: oidc.clientId,
    redirect_uri: redirectUri(),
    response_type: "code",
    // offline_access asks the issuer for a rotating refresh token so a page
    // reload is not a full re-login.
    scope: "openid email profile offline_access",
    state,
    code_challenge: b64url(new Uint8Array(digest)),
    code_challenge_method: "S256",
  });
  location.assign(`${oidc.issuer}/authorize?${params}`);
}

/** Completes the OIDC redirect: exchanges ?code through the API proxy with a
 *  form-encoded grant. Returns true when a session is established. Handles
 *  both redirect styles: code in the query (spec) or appended after the hash
 *  route (issuers that treat redirect_uri as an opaque string). */
export async function handleOidcCallback(): Promise<boolean> {
  const hashQuery = location.hash.includes("?") ? location.hash.slice(location.hash.indexOf("?") + 1) : "";
  const params = new URLSearchParams(location.search || hashQuery);
  const code = params.get("code");
  const state = params.get("state");
  if (!code) return false;
  if (!state || state !== sessionStorage.getItem(STATE_KEY)) return false;
  const verifier = sessionStorage.getItem(VERIFIER_KEY) || "";
  sessionStorage.removeItem(VERIFIER_KEY);
  sessionStorage.removeItem(STATE_KEY);
  const res = await exchange({
    grant_type: "authorization_code",
    code,
    code_verifier: verifier,
    redirect_uri: redirectUri(),
  });
  if (!res.ok) return false;
  adopt(await res.json(), "oidc");
  history.replaceState(null, "", location.pathname + "#/");
  return true;
}

function exchange(grant: Record<string, string>): Promise<Response> {
  return fetch(`${apiBase()}/v1/auth/exchange`, {
    method: "POST",
    headers: { "Content-Type": "application/x-www-form-urlencoded" },
    body: new URLSearchParams(grant).toString(),
  });
}

let refreshing: Promise<string> | null = null;

/** Returns a live access token, refreshing when within a minute of expiry.
 *  Empty string means the caller must send the user to login.
 *
 *  The refresh is single-flight: both backends rotate refresh tokens and may
 *  treat reuse of a rotated token as theft, so two concurrent API calls must
 *  never both present the same token. */
export async function getToken(): Promise<string> {
  if (accessToken && Date.now() < expiresAt - 60_000) return accessToken;
  refreshing ??= doRefresh().finally(() => {
    refreshing = null;
  });
  return refreshing;
}

/** With localStorage the refresh token is shared across tabs; a Web Lock
 *  makes the rotation exclusive browser-wide, and the state is re-checked
 *  inside the lock so a waiting tab picks up its sibling's rotation. */
async function doRefresh(): Promise<string> {
  if (typeof navigator !== "undefined" && navigator.locks) {
    return navigator.locks.request(LOCK_NAME, () => refreshExclusive());
  }
  return refreshExclusive();
}

async function refreshExclusive(): Promise<string> {
  if (accessToken && Date.now() < expiresAt - 60_000) return accessToken;
  const refresh = localStorage.getItem(REFRESH_KEY);
  if (!refresh) return "";
  const mode = (localStorage.getItem(MODE_KEY) as Mode) || "local";
  try {
    const res =
      mode === "oidc"
        ? await exchange({ grant_type: "refresh_token", refresh_token: refresh })
        : await fetch(`${apiBase()}/v1/auth/refresh`, {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ refreshToken: refresh }),
          });
    if (!res.ok) {
      // An invalid or rotated-away token means the session is truly gone;
      // transient server trouble (5xx) keeps it for a later attempt.
      if (res.status === 400 || res.status === 401) clearSession();
      return "";
    }
    adopt(await res.json(), mode);
    return accessToken;
  } catch {
    return ""; // network blip -- try again on the next call
  }
}

/** Drops only the in-memory access token so the next getToken() refreshes.
 *  Used on a 401 from the API; the refresh token must survive that. */
export function invalidateAccess(): void {
  accessToken = "";
  expiresAt = 0;
}

/** Display-only claims from the access token. The API re-verifies signature
 *  and roles on every request -- this only drives the UI shell. Accepts a
 *  "roles" array claim or a single "role" string. */
export function session(): Session | null {
  if (!accessToken) return null;
  try {
    const payload = JSON.parse(atob(accessToken.split(".")[1].replace(/-/g, "+").replace(/_/g, "/")));
    const roles = Array.isArray(payload.roles)
      ? payload.roles.map(String)
      : payload.role
        ? [String(payload.role)]
        : [];
    return { email: String(payload.email || payload.sub || ""), roles };
  } catch {
    return null;
  }
}

/** Roles are ranked server-side; moderator and above can triage the queue. */
export function canModerate(s: Session | null): boolean {
  return !!s && s.roles.some((r) => r === "moderator" || r === "librarian" || r === "admin");
}

function clearSession(): void {
  accessToken = "";
  expiresAt = 0;
  localStorage.removeItem(REFRESH_KEY);
  localStorage.removeItem(MODE_KEY);
}

/** Ends the session locally and, for local auth, revokes the refresh token
 *  server-side. */
export async function logout(): Promise<void> {
  const refresh = localStorage.getItem(REFRESH_KEY);
  const mode = localStorage.getItem(MODE_KEY);
  clearSession();
  if (refresh && mode === "local") {
    await fetch(`${apiBase()}/v1/auth/logout`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ refreshToken: refresh }),
    }).catch(() => undefined);
  }
}
