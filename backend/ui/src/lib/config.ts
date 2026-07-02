// Boot configuration. The SPA ships zero deployment specifics: everything
// (API base, auth modes, issuer, vocab schemes) arrives from GET /config at
// startup. Modules read the loaded value through getConfig(); tests inject
// one with setConfig().
import type { ClientConfig } from "./types";

const fallback: ClientConfig = { apiBase: "", localAuth: false, provider: "" };

let current: ClientConfig | null = null;

/** Fetches /config once and caches it for the session. */
export async function loadConfig(): Promise<ClientConfig> {
  if (current) return current;
  let loaded = fallback;
  try {
    const res = await fetch("/config");
    if (res.ok) loaded = { ...fallback, ...(await res.json()) };
  } catch {
    loaded = fallback;
  }
  current = loaded;
  return loaded;
}

/** The loaded config; the fallback before loadConfig resolves. */
export function getConfig(): ClientConfig {
  return current ?? fallback;
}

/** Test seam: replaces the loaded config (pass null to reset). */
export function setConfig(cfg: ClientConfig | null): void {
  current = cfg;
}

/** The API origin prefix ("" means same origin as the SPA). */
export function apiBase(): string {
  return getConfig().apiBase;
}
