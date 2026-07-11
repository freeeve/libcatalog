// Local mirror of the editor's draft: staged ops written to
// localStorage on every edit, so a reload -- crash, accidental Cmd-R, or the
// dead-session reload -- can offer the work back. The server
// draft (3s autosave) is the durable cross-device copy; this mirror is the
// layer that still works when the network or the session does not. Cleared
// on save, discard, and explicit sign-out (shared terminals).
import type { DraftBody } from "./types";

const PREFIX = "lcat-localdraft-";

interface StoredDraft {
  body: DraftBody;
  savedAt: string;
}

/** Writes the work's staged ops; an empty list removes the entry. */
export function saveLocalDraft(workId: string, body: DraftBody): void {
  try {
    if (!body.ops || body.ops.length === 0) {
      localStorage.removeItem(PREFIX + workId);
      return;
    }
    localStorage.setItem(PREFIX + workId, JSON.stringify({ body, savedAt: new Date().toISOString() } satisfies StoredDraft));
  } catch {
    // Quota or privacy mode: the mirror is best-effort by design.
  }
}

/** The work's mirrored draft, or null. */
export function loadLocalDraft(workId: string): { body: DraftBody; savedAt: string } | null {
  try {
    const raw = localStorage.getItem(PREFIX + workId);
    if (!raw) return null;
    const parsed = JSON.parse(raw) as StoredDraft;
    if (!parsed?.body?.ops?.length) return null;
    return parsed;
  } catch {
    return null;
  }
}

/** Drops one work's mirror. */
export function clearLocalDraft(workId: string): void {
  try {
    localStorage.removeItem(PREFIX + workId);
  } catch {
    // Nothing to do.
  }
}

/** Drops every mirror (explicit sign-out: shared terminals must not leak
 *  one cataloger's staged work into the next session). */
export function clearAllLocalDrafts(): void {
  try {
    for (const key of Object.keys(localStorage)) {
      if (key.startsWith(PREFIX)) localStorage.removeItem(key);
    }
  } catch {
    // Nothing to do.
  }
}
