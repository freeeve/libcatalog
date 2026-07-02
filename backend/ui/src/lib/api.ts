// Typed client for the cataloging API. Injects the bearer token and retries
// once through a refresh on 401; a second 401 surfaces as an ApiError the
// shell turns into the login screen.
import { apiBase } from "./config";
import { getToken, invalidateAccess } from "./auth";
import type { QueuePage, TermRef, WorkDocResponse, WorksPage } from "./types";

export class ApiError extends Error {
  constructor(
    public status: number,
    message: string,
  ) {
    super(message);
  }
}

async function call<T>(method: string, path: string, body?: unknown): Promise<T> {
  for (let attempt = 0; attempt < 2; attempt++) {
    const token = await getToken();
    if (!token) throw new ApiError(401, "not signed in");
    const res = await fetch(apiBase() + path, {
      method,
      headers: {
        Authorization: `Bearer ${token}`,
        ...(body !== undefined ? { "Content-Type": "application/json" } : {}),
      },
      body: body !== undefined ? JSON.stringify(body) : undefined,
    });
    if (res.status === 401 && attempt === 0) {
      invalidateAccess(); // retry refreshes via the (still valid) refresh token
      continue;
    }
    if (!res.ok) {
      const detail = await res.json().catch(() => ({}) as { error?: string });
      throw new ApiError(res.status, detail.error || res.statusText);
    }
    return (await res.json()) as T;
  }
  throw new ApiError(401, "authentication failed");
}

/** Work search over the grain tree (librarian). */
export function fetchWorks(q: string, limit = 50): Promise<WorksPage> {
  const params = new URLSearchParams({ q, limit: String(limit) });
  return call("GET", `/v1/works?${params}`);
}

/** The profile-shaped editing document for one work (librarian). */
export function fetchWorkDoc(id: string): Promise<WorkDocResponse> {
  return call("GET", `/v1/works/${encodeURIComponent(id)}/doc`);
}

/** The suggestion review queue (moderator). */
export function fetchQueue(params: { status?: string; cursor?: string; limit?: number } = {}): Promise<QueuePage> {
  const q = new URLSearchParams({ status: params.status ?? "PENDING" });
  if (params.cursor) q.set("cursor", params.cursor);
  if (params.limit) q.set("limit", String(params.limit));
  return call("GET", `/v1/queue?${q}`);
}

/** Controlled-vocabulary autocomplete. */
export function searchTerms(scheme: string, q: string): Promise<{ terms: TermRef[] }> {
  const params = new URLSearchParams({ scheme, q });
  return call("GET", `/v1/terms?${params}`);
}
