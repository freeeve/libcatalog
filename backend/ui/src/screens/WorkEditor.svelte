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
  import ProfileForm from "../components/ProfileForm.svelte";
  import SaveBar from "../components/SaveBar.svelte";
  import { createEditorSession } from "../lib/editor";
  import { bindKeys, popScope, pushScope } from "../lib/keyboard";

  let { workId }: { workId: string } = $props();

  const SCOPE = "editor";
  // The mount is keyed on workId in App.svelte, so one session per work.
  // svelte-ignore state_referenced_locally
  const session = createEditorSession(workId);

  let tab = $state<"native" | "history">("native");

  onMount(() => {
    pushScope(SCOPE);
    const unbind = bindKeys(SCOPE, {
      p: { description: "preview staged changes", handler: () => void session.preview() },
    });
    // Ctrl/Cmd+S must fire even while focus sits in a form input, so it
    // bypasses the scope dispatcher (which ignores modified keys).
    const onModS = (ev: KeyboardEvent): void => {
      if ((ev.metaKey || ev.ctrlKey) && !ev.altKey && ev.key.toLowerCase() === "s") {
        ev.preventDefault();
        void session.save();
      }
    };
    window.addEventListener("keydown", onModS);
    void session.load();
    return () => {
      unbind();
      popScope(SCOPE);
      window.removeEventListener("keydown", onModS);
      session.destroy();
    };
  });
</script>

<main>
  <p><a href="#/works">← Back to search</a></p>

  {#if $session.loading}
    <p class="muted" aria-live="polite">Loading {workId}…</p>
  {:else if $session.loadError}
    <p class="error" role="alert">{$session.loadError}</p>
  {:else if $session.doc}
    {@const doc = $session.doc}
    <article aria-label={"Work " + doc.workId}>
      <header class="dochead">
        <h1>{doc.workId}</h1>
        <p class="muted">profile {doc.profileId} · etag <code>{$session.etag}</code></p>
      </header>

      {#if $session.pendingDraft}
        <div class="banner" role="status">
          <span>
            You have a draft for this work ({$session.pendingDraft.body?.ops?.length ?? 0} edits, saved
            {new Date($session.pendingDraft.updatedAt).toLocaleString()}).
          </span>
          <button class="button" onclick={() => session.resumeDraft()}>
            Resume draft ({$session.pendingDraft.body?.ops?.length ?? 0} edits)
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

      {#if $session.notice}<p class="notice" role="status">{$session.notice}</p>{/if}
      {#if $session.opError}<p class="error" role="alert">{$session.opError}</p>{/if}

      <div class="tabs" role="group" aria-label="Editor view">
        <button class="tab" class:active={tab === "native"} aria-pressed={tab === "native"} onclick={() => (tab = "native")}>
          Native
        </button>
        <button class="tab" class:active={tab === "history"} aria-pressed={tab === "history"} onclick={() => (tab = "history")}>
          History
        </button>
      </div>

      {#if tab === "native"}
        <section aria-label="Work fields">
          <ProfileForm
            res={doc.work}
            resource="work"
            kind="work"
            ops={$session.ops}
            onstage={(op) => session.stage(op)}
            onunstage={(op) => session.unstage(op)}
          />
        </section>

        {#if doc.instances.length > 0}
          <section aria-label="Instances">
            <h2 class="instances-head">Instances</h2>
            {#each doc.instances as inst (inst.id)}
              <div class="instance">
                <h3>{inst.id}</h3>
                <ProfileForm
                  res={inst}
                  resource={inst.id}
                  kind="instance"
                  ops={$session.ops}
                  onstage={(op) => session.stage(op)}
                  onunstage={(op) => session.unstage(op)}
                />
              </div>
            {/each}
          </section>
        {/if}

        {#if doc.passthrough.length > 0}
          <details class="passthrough">
            <summary>Passthrough ({doc.passthrough.length} statements)</summary>
            <pre>{doc.passthrough.join("\n")}</pre>
          </details>
        {/if}

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
      {:else}
        <HistoryPanel {workId} />
      {/if}
    </article>
  {/if}
</main>

<style>
  .dochead h1 {
    margin-bottom: 0.1rem;
    font-family: var(--mono);
    font-size: 1.15rem;
  }
  .dochead p {
    margin-top: 0;
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
    background: #fdf3dc;
    border-color: #ecd9a6;
    color: #4a3608;
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
  }
  .instance h3 {
    font-family: var(--mono);
    font-size: 0.95rem;
    margin: 0.5rem 0 0.25rem;
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
