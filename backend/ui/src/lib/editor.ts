// Editor session for one work: the staged-ops store plus the network flows
// around it -- doc load, dry-run preview, the If-Match save with the 412
// rebase path, and debounced draft autosave. The server owns the override
// semantics; this store only sequences requests and mirrors their state.
import { writable, type Readable } from "svelte/store";
import {
  ApiError,
  ConflictError,
  createDraft,
  deleteDraft,
  fetchDrafts,
  fetchWorkDoc,
  postOps,
  updateDraft,
} from "./api";
import { isSandbox } from "./config";
import { clearLocalDraft, loadLocalDraft, saveLocalDraft } from "./localdraft";
import { createOpsStore } from "./ops";
import type { Diff, Draft, DuplicateMatch, Op, WorkDoc } from "./types";

export const AUTOSAVE_MS = 3000;

export interface EditorState {
  loading: boolean;
  loadError: string;
  doc: WorkDoc | null;
  etag: string;
  ops: Op[];
  /** A save, preview, or conflict reload in flight. */
  busy: boolean;
  /** The last dry-run delta; cleared by further edits. */
  diff: Diff | null;
  /** A 412 happened: the record moved underneath the staged ops. */
  conflict: boolean;
  /** Server message from a failed preview/save (validation, 400s). */
  opError: string;
  /** Last successful-save summary. */
  notice: string;
  /** Non-blocking post-save warning: the doc now clusters with another
   *  work (tasks/068). Dismissed by further edits or dismissDuplicate. */
  duplicate: DuplicateMatch | null;
  /** A draft found on open, awaiting the resume-or-discard choice. */
  pendingDraft: Draft | null;
  /** The autosave target draft id ("" until the first autosave lands). */
  draftId: string;
}

export interface EditorSession extends Readable<EditorState> {
  load(): Promise<void>;
  stage(op: Op): void;
  unstage(op: Op): void;
  /** Dry-runs the staged ops; the diff (or the server's objection) lands
   *  in state. */
  preview(): Promise<void>;
  /** Ships the staged ops under If-Match; true on success. */
  save(): Promise<boolean>;
  /** Hides the dry-run diff panel without touching the staged ops. */
  dismissPreview(): void;
  /** Conflict recovery: refetch the doc, keep the staged ops, and replay
   *  them as a dry run so stale ops surface the server's message. */
  reload(): Promise<void>;
  /** Drops the staged ops and the autosaved draft. */
  discard(): Promise<void>;
  /** Hides the duplicate warning banner. */
  dismissDuplicate(): void;
  resumeDraft(): void;
  discardDraft(): Promise<void>;
  /** Cancels the pending autosave timer (screen unmount). */
  destroy(): void;
}

/** A fresh session for one work (one per editor screen mount). */
export function createEditorSession(workId: string): EditorSession {
  const ops = createOpsStore();
  let state: EditorState = {
    loading: true,
    loadError: "",
    doc: null,
    etag: "",
    ops: [],
    busy: false,
    diff: null,
    conflict: false,
    opError: "",
    notice: "",
    duplicate: null,
    pendingDraft: null,
    draftId: "",
  };
  const { subscribe, set } = writable(state);
  const patch = (p: Partial<EditorState>): void => {
    state = { ...state, ...p };
    set(state);
  };
  ops.subscribe((list) => patch({ ops: list }));

  let timer: ReturnType<typeof setTimeout> | undefined;
  // Sandbox demo: edits "saved" this session are folded into this list and
  // replayed as a dry run so the rendered doc stays cumulative -- never
  // persisted, wiped when the screen remounts (a page refresh).
  let sandboxOps: Op[] = [];

  function scheduleAutosave(): void {
    if (isSandbox()) return; // drafts don't persist in the demo
    clearTimeout(timer);
    timer = setTimeout(() => void autosave(), AUTOSAVE_MS);
  }

  /** Best-effort create-or-update of the (work, user) draft; an empty
   *  staged list deletes it instead. Failures wait for the next edit. */
  async function autosave(): Promise<void> {
    const staged = ops.payload();
    try {
      if (staged.length === 0) {
        if (state.draftId) {
          const id = state.draftId;
          patch({ draftId: "" });
          await deleteDraft(id);
        }
        return;
      }
      const body = { baseEtag: state.etag, ops: staged };
      if (state.draftId) {
        await updateDraft(state.draftId, workId, body);
      } else if (state.pendingDraft) {
        // Editing fresh past the resume offer adopts the (work, user) draft
        // slot rather than piling up a second draft.
        const id = state.pendingDraft.id;
        patch({ draftId: id, pendingDraft: null });
        await updateDraft(id, workId, body);
      } else {
        const d = await createDraft(workId, body);
        patch({ draftId: d.id });
      }
    } catch {
      // Autosave stays silent; the ops are still staged locally.
    }
  }

  async function load(): Promise<void> {
    patch({ loading: true, loadError: "" });
    try {
      const res = await fetchWorkDoc(workId);
      let pendingDraft: Draft | null = null;
      try {
        const { drafts } = await fetchDrafts();
        pendingDraft = drafts.find((d) => d.workId === workId) ?? null;
      } catch {
        // Drafts are a convenience; the editor works without them.
      }
      // The local mirror wins over the server draft in this browser: it is
      // written on every edit, while the autosave lags 3s and dies with the
      // session (tasks/225). Resuming it re-adopts the server draft slot.
      const local = isSandbox() ? null : loadLocalDraft(workId);
      if (local) {
        pendingDraft = { id: pendingDraft?.id ?? "", workId, body: local.body, updatedAt: local.savedAt };
      }
      patch({ loading: false, doc: res.doc, etag: res.etag, pendingDraft });
    } catch (e) {
      patch({
        loading: false,
        loadError: e instanceof ApiError && e.status === 404 ? `No work ${workId}.` : `Failed to load ${workId}.`,
      });
    }
  }

  function afterEdit(): void {
    patch({ diff: null, opError: "", notice: "", duplicate: null });
    // Mirror synchronously: the point is surviving an abrupt reload, which
    // the 3s autosave debounce would lose (tasks/225).
    if (!isSandbox()) saveLocalDraft(workId, { baseEtag: state.etag, ops: ops.payload() });
    scheduleAutosave();
  }

  async function preview(): Promise<void> {
    const staged = ops.payload();
    if (staged.length === 0 || state.busy) return;
    patch({ busy: true, opError: "" });
    try {
      const res = await postOps(workId, staged, { dryRun: true });
      patch({ busy: false, diff: res.diff });
    } catch (e) {
      patch({ busy: false, diff: null, opError: e instanceof ApiError ? e.message : "preview failed" });
    }
  }

  // sandboxSave renders the staged edits as if committed -- it dry-runs the full
  // accumulated set and swaps in the materialized doc -- but never writes. A
  // page refresh remounts a fresh session and the record is pristine again.
  async function sandboxSave(staged: Op[]): Promise<boolean> {
    patch({ busy: true, opError: "", notice: "" });
    const all = [...sandboxOps, ...staged];
    try {
      const res = await postOps(workId, all, { dryRun: true });
      sandboxOps = all;
      ops.clear();
      clearTimeout(timer);
      patch({
        busy: false,
        doc: res.doc ?? state.doc,
        diff: null,
        draftId: "",
        duplicate: res.duplicate ?? null,
        notice: `Rendered ${staged.length} edit${staged.length === 1 ? "" : "s"} in the demo -- not saved. Refresh to reset.`,
      });
      return true;
    } catch (e) {
      patch({ busy: false, opError: e instanceof ApiError ? e.message : "preview failed" });
      return false;
    }
  }

  async function save(): Promise<boolean> {
    const staged = ops.payload();
    if (staged.length === 0 || state.busy) return false;
    if (isSandbox()) return sandboxSave(staged);
    patch({ busy: true, opError: "", notice: "" });
    try {
      const res = await postOps(workId, staged, { ifMatch: state.etag });
      clearTimeout(timer);
      const draftId = state.draftId;
      ops.clear();
      clearLocalDraft(workId);
      patch({
        busy: false,
        etag: res.etag,
        diff: null,
        conflict: false,
        draftId: "",
        duplicate: res.duplicate ?? null,
        notice: `Saved ${staged.length} edit${staged.length === 1 ? "" : "s"} (+${res.diff.added.length} / -${res.diff.removed.length} statements).`,
      });
      if (draftId) await deleteDraft(draftId).catch(() => undefined);
      try {
        const fresh = await fetchWorkDoc(workId); // overrides now show struck through
        patch({ doc: fresh.doc, etag: fresh.etag });
      } catch {
        // The save landed; a failed refetch just leaves the stale view.
      }
      return true;
    } catch (e) {
      if (e instanceof ConflictError) {
        patch({ busy: false, conflict: true });
        return false;
      }
      patch({ busy: false, opError: e instanceof ApiError ? e.message : "save failed" });
      return false;
    }
  }

  async function reload(): Promise<void> {
    patch({ busy: true, opError: "", diff: null });
    try {
      const res = await fetchWorkDoc(workId);
      patch({ busy: false, doc: res.doc, etag: res.etag, conflict: false });
    } catch {
      patch({ busy: false, opError: "reload failed" });
      return;
    }
    await preview();
  }

  async function discard(): Promise<void> {
    clearTimeout(timer);
    ops.clear();
    clearLocalDraft(workId);
    const draftId = state.draftId;
    patch({ diff: null, opError: "", conflict: false, draftId: "" });
    if (draftId) await deleteDraft(draftId).catch(() => undefined);
    // Sandbox: also drop the demo-committed edits and re-show the pristine doc.
    if (isSandbox() && sandboxOps.length) {
      sandboxOps = [];
      try {
        const fresh = await fetchWorkDoc(workId);
        patch({ doc: fresh.doc, etag: fresh.etag });
      } catch {
        // Best effort; the next refresh resets anyway.
      }
    }
  }

  function resumeDraft(): void {
    const d = state.pendingDraft;
    if (!d) return;
    ops.load(d.body?.ops ?? []);
    patch({
      pendingDraft: null,
      draftId: d.id,
      notice:
        d.body?.baseEtag && d.body.baseEtag !== state.etag
          ? "The draft predates the current record version--preview to revalidate it."
          : "",
    });
  }

  async function discardDraft(): Promise<void> {
    const d = state.pendingDraft;
    patch({ pendingDraft: null });
    clearLocalDraft(workId);
    if (d?.id) await deleteDraft(d.id).catch(() => undefined);
  }

  return {
    subscribe,
    load,
    stage(op: Op): void {
      ops.stage(op);
      afterEdit();
    },
    unstage(op: Op): void {
      ops.unstage(op);
      afterEdit();
    },
    preview,
    save,
    dismissPreview(): void {
      patch({ diff: null });
    },
    dismissDuplicate(): void {
      patch({ duplicate: null });
    },
    reload,
    discard,
    resumeDraft,
    discardDraft,
    destroy(): void {
      clearTimeout(timer);
    },
  };
}
