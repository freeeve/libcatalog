<script lang="ts">
  // Editable work editor (task 045): the WorkDoc renders as a profile form
  // whose edits stage locally as field ops; the sticky save bar previews the
  // exact quad delta (dry run) and ships the batch under If-Match. A 412
  // opens the rebase banner (reload keeps the staged ops and replays them);
  // drafts autosave in the background and offer to resume on open. The
  // History tab lists the work's audit trail. Keys: p previews, Ctrl/Cmd+S
  // saves, Esc closes the picker modal.
  import { onMount } from "svelte";
  import DiffPreview from "../components/DiffPreview.svelte";
  import HistoryPanel from "../components/HistoryPanel.svelte";
  import ItemsPanel from "../components/ItemsPanel.svelte";
  import MacroBar from "../components/MacroBar.svelte";
  import MarcPanel from "../components/MarcPanel.svelte";
  import MarcPreviewPane from "../components/MarcPreviewPane.svelte";
  import ProfileForm from "../components/ProfileForm.svelte";
  import SaveBar from "../components/SaveBar.svelte";
  import SubjectLookup from "../components/SubjectLookup.svelte";
  import VisibilityPanel from "../components/VisibilityPanel.svelte";
  import CoverPanel from "../components/CoverPanel.svelte";
  import RelationsPanel from "../components/RelationsPanel.svelte";
  import SimilarPanel from "../components/SimilarPanel.svelte";
  import AttachmentsPanel from "../components/AttachmentsPanel.svelte";
  import { cloneWork, fetchIdentifierKinds, fetchItems, splitWork, ApiError } from "../lib/api";
  import type { SubjectCandidate } from "../lib/types";
  import { isReadOnly } from "../lib/config";
  import { createEditorSession } from "../lib/editor";
  import { EDITOR_CHORDS, bindKeys, popScope, pushScope } from "../lib/keyboard";
  import { navigate, setLeaveGuard } from "../lib/router";

  let { workId }: { workId: string } = $props();

  const readOnly = isReadOnly();

  const SCOPE = "editor";
  // The mount is keyed on workId in App.svelte, so one session per work.
  // svelte-ignore state_referenced_locally
  const session = createEditorSession(workId);

  // Staged-ops guard (tasks/199): in-app navigation asks before discarding
  // and beforeunload arms the browser's native leave prompt while dirty.
  // The background draft autosave usually makes a discard recoverable; the
  // prompt keeps it deliberate.
  const dirty = $derived($session.ops.length > 0);
  $effect(() => {
    if (!dirty) return;
    const unregister = setLeaveGuard(() => confirm("Discard unsaved changes to this record?"));
    const onBeforeUnload = (ev: BeforeUnloadEvent) => ev.preventDefault();
    window.addEventListener("beforeunload", onBeforeUnload);
    return () => {
      unregister();
      window.removeEventListener("beforeunload", onBeforeUnload);
    };
  });

  let tab = $state<"native" | "marc" | "history">("native");
  // The live MARC pane (tasks/070): read-only, side by side on wide screens.
  let marcPane = $state(false);
  let itemCounts = $state<Record<string, number>>({});
  let idKinds = $state<Record<string, string>>({});

  /** External heading -> staged op: controlled subject when reconciled,
   *  tag otherwise (tasks/073). */
  function addLookedUpSubject(c: SubjectCandidate): void {
    if (c.term) {
      session.stage({ resource: "work", path: "subjects", action: "add", value: { v: c.term.id, iri: true } });
    } else {
      session.stage({ resource: "work", path: "tags", action: "add", value: { v: c.heading } });
    }
  }
  let cloneError = $state("");

  /** Clones the saved grain into a fresh suppressed draft and opens it.
   *  Disabled while edits are staged: the clone copies the store's bytes,
   *  so unsaved ops would silently stay behind. */
  async function doClone(): Promise<void> {
    cloneError = "";
    try {
      const res = await cloneWork(workId);
      navigate(`/works/${res.workId}`);
    } catch (e) {
      cloneError = e instanceof ApiError ? e.message : "clone failed";
    }
  }

  let splitPick = $state<Record<string, boolean>>({});
  let splitNotice = $state("");
  let splitError = $state("");
  // Guards the split button while the request is in flight: the server makes a
  // repeated split idempotent (tasks/323), but there is no reason to fire the second
  // request at all, and a live button after a click reads as "nothing happened".
  let splitBusy = $state(false);

  const splitCount = $derived(Object.values(splitPick).filter(Boolean).length);

  async function doSplit(): Promise<void> {
    const instances = Object.entries(splitPick)
      .filter(([, v]) => v)
      .map(([k]) => k);
    if (instances.length === 0 || splitBusy) return;
    splitError = "";
    splitBusy = true;
    try {
      const res = await splitWork(workId, instances);
      splitNotice = `split recorded -- ${instances.length} instance${instances.length === 1 ? "" : "s"} pin to ${res.newWork}; the new work materializes on the next ingest`;
      splitPick = {};
    } catch (e) {
      splitError = e instanceof ApiError ? e.message : "split failed";
    } finally {
      splitBusy = false;
    }
  }

  onMount(() => {
    pushScope(SCOPE);
    // Keys come from EDITOR_CHORDS, the table the macro screen validates
    // against, so a shortcut can never be offered for a chord bound here.
    const unbind = bindKeys(SCOPE, {
      "1": { description: EDITOR_CHORDS["1"], legend: "tabs", keyLabel: "1/2/3", handler: () => (tab = "native") },
      // legendHidden, not hidden: the footer rail shows one "1/2/3 tabs" row,
      // but the "?" overlay -- the only place to look up what a key does --
      // must name each tab. These were invisible there (tasks/237).
      "2": { description: EDITOR_CHORDS["2"], legendHidden: true, handler: () => (tab = "marc") },
      "3": { description: EDITOR_CHORDS["3"], legendHidden: true, handler: () => (tab = "history") },
      p: { description: EDITOR_CHORDS.p, legend: "preview", handler: () => void session.preview() },
      m: { description: EDITOR_CHORDS.m, legend: "marc pane", handler: () => (marcPane = !marcPane) },
      "mod+s": { description: "save staged changes", legend: "save", handler: () => void session.save() },
    });
    void session.load();
    fetchItems(workId).then(
      (res) => {
        const counts: Record<string, number> = {};
        for (const [inst, list] of Object.entries(res.items ?? {})) counts[inst] = list.length;
        itemCounts = counts;
      },
      () => {},
    );
    fetchIdentifierKinds(workId).then(
      (res) => (idKinds = res.kinds ?? {}),
      () => {},
    );
    return () => {
      unbind();
      popScope(SCOPE);
      session.destroy();
    };
  });
</script>

<main class="wide" id="main" tabindex="-1">
  <p class="back"><a href="#/works">← Back to search</a></p>

  {#if $session.loading}
    <p class="muted" aria-live="polite">Loading {workId}…</p>
  {:else if $session.loadError}
    <p class="error" role="alert">{$session.loadError}</p>
  {:else if $session.doc}
    {@const doc = $session.doc}
    {@const title = doc.work.fields["title"]?.[0]?.v}
    <article aria-label={"Work " + doc.workId}>
      <header class="dochead">
        <h1 title={title || doc.workId}>{title || doc.workId}</h1>
        <code class="docid">{doc.workId}</code>
        <span class="muted meta">profile {doc.profileId}</span>
        <span class="muted meta" title={"etag " + $session.etag}>etag <code>{$session.etag.slice(0, 10)}</code></span>
        <span class="spacer"></span>
        {#if $session.ops.length > 0}
          <span class="stagedct">{$session.ops.length} staged</span>
        {/if}
        {#if $session.busy}<span class="muted meta" role="status">working…</span>{/if}
        {#if !readOnly}
          <button
            class="button button--quiet"
            onclick={() => void doClone()}
            disabled={$session.busy || $session.ops.length > 0}
            title={$session.ops.length > 0
              ? "save or discard staged edits first"
              : "copy into a new suppressed draft with fresh ids: description, subject and genre headings come along; identifiers, holdings and work links stay here"}
          >
            Clone
          </button>
        {/if}
      </header>
      {#if cloneError}
        <p class="error" role="alert">{cloneError}</p>
      {/if}
      <details class="vis">
        <summary>Visibility</summary>
        <VisibilityPanel {workId} />
        <CoverPanel {workId} cover={$session.cover} />
      </details>
      <details class="vis">
        <summary>Relationships</summary>
        <RelationsPanel {workId} />
      </details>
      <details class="vis">
        <summary>More like this</summary>
        <SimilarPanel {workId} />
      </details>
      <details class="vis">
        <summary>Attachments</summary>
        <AttachmentsPanel {workId} />
      </details>

      {#if $session.pendingDraft}
        {@const draftOps = $session.pendingDraft.body?.ops?.length ?? 0}
        <div class="banner" role="status">
          <span>
            You have a draft for this work ({draftOps} edit{draftOps === 1 ? "" : "s"}, saved
            {new Date($session.pendingDraft.updatedAt).toLocaleString()}).
          </span>
          <button class="button" onclick={() => session.resumeDraft()}>
            Resume draft ({draftOps} edit{draftOps === 1 ? "" : "s"})
          </button>
          <button class="button button--quiet" onclick={() => void session.discardDraft()}>Discard draft</button>
        </div>
      {/if}

      {#if $session.conflict}
        <div class="banner banner--warn" role="alert">
          <span>This record changed while you were editing.</span>
          <button class="button" onclick={() => void session.reload()} disabled={$session.busy}>Reload</button>
          <button class="button button--quiet" onclick={() => void session.discard()} disabled={$session.busy}>
            Discard my edits
          </button>
        </div>
      {/if}

      {#if $session.duplicate}
        <div class="banner banner--warn" role="alert">
          <span>
            Saved -- but this record now matches an existing work
            ({$session.duplicate.via === "identifier" ? "shared identifier" : "same title and author"}).
          </span>
          <a class="button" href="#/works/{$session.duplicate.workId}">Open {$session.duplicate.workId}</a>
          <button class="button button--quiet" onclick={() => session.dismissDuplicate()}>Dismiss</button>
        </div>
      {/if}

      {#if $session.notice}<p class="notice" role="status">{$session.notice}</p>{/if}
      {#if $session.opError}<p class="error" role="alert">{$session.opError}</p>{/if}

      <div class="tabs" role="group" aria-label="Editor view">
        <button class="tab" class:active={tab === "native"} aria-pressed={tab === "native"} onclick={() => (tab = "native")}>
          Native
        </button>
        <button class="tab" class:active={tab === "marc"} aria-pressed={tab === "marc"} onclick={() => (tab = "marc")}>
          MARC
        </button>
        <button class="tab" class:active={tab === "history"} aria-pressed={tab === "history"} onclick={() => (tab = "history")}>
          History
        </button>
        {#if tab === "native"}
          <span class="tab-spacer"></span>
          <button class="tab" class:active={marcPane} aria-pressed={marcPane} onclick={() => (marcPane = !marcPane)}>
            MARC preview
          </button>
        {/if}
      </div>

      {#if tab === "native"}
        <div class="cols" class:with-pane={marcPane}>
        <div class="native-col">
        <section aria-label="Work fields">
          <ProfileForm
            res={doc.work}
            resource="work"
            kind="work"
            ops={$session.ops}
            onstage={(op) => session.stage(op)}
            onunstage={(op) => session.unstage(op)}
          />
          <SubjectLookup {workId} onadd={addLookedUpSubject} />
        </section>

        {#if doc.instances.length > 0}
          <section aria-label="Instances">
            <h2 class="instances-head">Instances</h2>
            {#each doc.instances as inst (inst.id)}
              <details class="instance" open={doc.instances.length === 1}>
                <summary>
                  <h3>
                    <span class="inst-eyebrow">Instance</span>
                    <code>{inst.id}</code>
                    <span class="muted inst-meta">
                      {itemCounts[inst.id] ?? 0} item{(itemCounts[inst.id] ?? 0) === 1 ? "" : "s"}
                    </span>
                  </h3>
                </summary>
                {#if doc.instances.length > 1}
                  <label class="split-pick">
                    <input type="checkbox" bind:checked={splitPick[inst.id]} /> select for split
                  </label>
                {/if}
                <ProfileForm
                  res={inst}
                  resource={inst.id}
                  kind="instance"
                  ops={$session.ops}
                  onstage={(op) => session.stage(op)}
                  onunstage={(op) => session.unstage(op)}
                  {idKinds}
                />
                <ItemsPanel {workId} instanceId={inst.id} />
              </details>
            {/each}
            {#if doc.instances.length > 1 && !readOnly}
              <p class="split-bar">
                <button class="button button--quiet" onclick={() => void doSplit()} disabled={splitCount === 0 || splitBusy}>
                  Split {splitCount || ""} selected instance{splitCount === 1 ? "" : "s"} into a new work
                </button>
                <span aria-live="polite">
                  {#if splitNotice}<span class="notice">{splitNotice}</span>{/if}
                  {#if splitError}<span class="error">{splitError}</span>{/if}
                </span>
              </p>
            {/if}
          </section>
        {/if}

        {#if doc.passthrough.length > 0}
          <details class="passthrough">
            <summary>Passthrough ({doc.passthrough.length} statements)</summary>
            <pre>{doc.passthrough.join("\n")}</pre>
          </details>
        {/if}

        <MacroBar ops={$session.ops} onapply={(op) => session.stage(op)} />

        {#if $session.diff}
          <DiffPreview diff={$session.diff} onclose={() => session.dismissPreview()} />
        {/if}

        <SaveBar
          count={$session.ops.length}
          busy={$session.busy}
          onpreview={() => void session.preview()}
          onsave={() => void session.save()}
          ondiscard={() => void session.discard()}
        />
        </div>
        {#if marcPane}
          <aside class="marc-pane-col" aria-label="Live MARC preview">
            <MarcPreviewPane {workId} ops={$session.ops} />
          </aside>
        {/if}
        </div>
      {:else if tab === "marc"}
        <MarcPanel {workId} scope={SCOPE} />
      {:else}
        <HistoryPanel {workId} />
      {/if}
    </article>
  {/if}
</main>

<style>
  /* Sticky record identity: title, id, etag, and staged count never scroll
     away while a long record is edited. */
  .dochead {
    position: sticky;
    top: 0;
    z-index: 5;
    display: flex;
    align-items: baseline;
    gap: 0.7rem;
    background: var(--bg);
    border-bottom: 3px double var(--rule);
    padding: 0.35rem 0 0.3rem;
  }
  .dochead h1 {
    margin: 0;
    padding: 0;
    border-bottom: none;
    font-size: 1.15rem;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
    max-width: 34rem;
  }
  .meta {
    font-size: 0.78rem;
    white-space: nowrap;
  }
  .meta code {
    font-size: 0.95em;
  }
  .docid {
    background: var(--surface-alt);
    border-radius: var(--radius);
    padding: 0.1em 0.45em;
    font-size: 0.78rem;
  }
  .spacer {
    flex: 1;
  }
  .stagedct {
    font-size: 0.75rem;
    font-weight: 650;
    color: var(--pend-ink);
    background: var(--pend-bg);
    border: 1px solid var(--pend-edge);
    border-radius: 999px;
    padding: 0.05em 0.6em;
    white-space: nowrap;
  }
  .vis {
    margin: 0.4rem 0;
  }
  .vis summary {
    cursor: pointer;
    color: var(--ink-muted);
    font-size: 0.85rem;
  }
  .back {
    margin: 0 0 0.3rem;
    font-size: 0.85rem;
  }
  .banner {
    display: flex;
    align-items: center;
    gap: 0.7rem;
    flex-wrap: wrap;
    border: 1px solid var(--rule);
    border-radius: 6px;
    background: var(--surface);
    padding: 0.55rem 0.8rem;
    margin: 0.6rem 0;
  }
  .banner span {
    flex: 1;
    min-width: 14rem;
  }
  .banner--warn {
    background: var(--pend-bg);
    border-color: var(--pend-edge);
    color: var(--pend-ink);
  }
  .notice {
    color: var(--ok);
    font-weight: 600;
  }
  .tabs {
    display: flex;
    gap: 0.4rem;
    margin: 0.75rem 0 0.25rem;
    border-bottom: 1px solid var(--rule);
    padding-bottom: 0.5rem;
  }
  .tab {
    background: var(--surface);
    border: 1px solid var(--rule);
    border-radius: 999px;
    padding: 0.2em 0.9em;
    color: var(--ink-muted);
    font-size: 0.85rem;
    font-weight: 600;
  }
  .tab.active {
    background: var(--accent);
    border-color: var(--accent);
    color: var(--accent-ink);
  }
  .instances-head {
    border-top: 1px solid var(--rule);
    padding-top: 1rem;
    margin-top: 1.5rem;
  }
  .instance {
    border: 1px solid var(--rule);
    border-radius: 6px;
    padding: 0.4rem 1rem 0.7rem;
    margin: 0.75rem 0;
    background: var(--surface);
  }
  .instance summary {
    cursor: pointer;
    margin: 0.15rem 0;
  }
  .instance summary h3 {
    display: inline-flex;
    align-items: center;
    gap: 0.5rem;
    font-size: 0.95rem;
    margin: 0;
  }
  .instance summary code {
    font-size: 0.85rem;
    color: var(--ink-muted);
  }
  .inst-meta {
    font-size: 0.8rem;
  }
  .split-pick {
    display: inline-flex;
    align-items: center;
    gap: 0.35rem;
    font-size: 0.8rem;
    color: var(--ink-muted);
    margin: 0.25rem 0;
  }
  .tab-spacer {
    flex: 1;
  }
  /* tasks/070: the live MARC pane sits beside the native form on wide
     viewports and stacks below it on narrow ones. */
  .cols.with-pane {
    display: grid;
    grid-template-columns: minmax(0, 3fr) minmax(0, 2fr);
    gap: 1.4rem;
    align-items: start;
  }
  .marc-pane-col {
    position: sticky;
    top: 3rem;
  }
  @media (max-width: 72rem) {
    .cols.with-pane {
      grid-template-columns: 1fr;
    }
    .marc-pane-col {
      position: static;
    }
  }
  .inst-eyebrow {
    font-size: 0.72rem;
    font-weight: 650;
    text-transform: uppercase;
    letter-spacing: 0.07em;
    color: var(--ink-muted);
  }
  .passthrough {
    margin-top: 1.5rem;
  }
  .passthrough summary {
    cursor: pointer;
    color: var(--ink-muted);
  }
  .passthrough pre {
    font-family: var(--mono);
    font-size: 0.78rem;
    background: var(--surface);
    border: 1px solid var(--rule);
    border-radius: 6px;
    padding: 0.75rem;
    overflow-x: auto;
  }
</style>
