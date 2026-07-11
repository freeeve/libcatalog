<script lang="ts">
  // The MARC tab of the dual-view editor: loads the grain's
  // records as field arrays, hosts the grid or the mrk-style text surface
  // per record -- two views of the same in-memory doc, so edits
  // carry across the toggle -- previews the exact quad delta (dry run), and
  // saves under If-Match. A text buffer that does not parse blocks preview
  // and save with its line-anchored errors. Untouched saves are no-ops
  // server-side; a concurrent edit reloads with a notice.
  import { onMount } from "svelte";
  import { ApiError, ConflictError, fetchMarc, postMarc } from "../lib/api";
  import { isReadOnly, isSandbox } from "../lib/config";
  import DiffPreview from "./DiffPreview.svelte";
  import FieldClipboardPane from "./FieldClipboardPane.svelte";
  import MarcGrid from "./MarcGrid.svelte";
  import MarcTextEditor from "./MarcTextEditor.svelte";
  import type { Diff, MarcField, MarcRecordDoc } from "../lib/types";

  let { workId, scope }: { workId: string; scope?: string } = $props();

  // The same gating SaveBar applies. A read-only demo hides Save;
  // the execute path is refused by the server anyway, and offering a control
  // that cannot work is what made the refusal look like a crash. Sandbox saves
  // dry-run and render the delta, never persisting. Preview stays in both --
  // it is why the read-only guard allowlists this route at all.
  const readOnly = isReadOnly();
  const sandbox = isSandbox();

  let etag = $state("");
  let records = $state<MarcRecordDoc[]>([]);
  let knownLoss = $state<Record<string, string>>({});
  let active = $state(0);
  let diff = $state<Diff | null>(null);
  let loading = $state(true);
  let busy = $state(false);
  let status = $state("");
  let error = $state("");
  let mode = $state<"grid" | "text">("grid");
  let textValid = $state(true);

  const blocked = $derived(mode === "text" && !textValid);

  /** A clipboard-pane paste appends to the active record; in text mode the
   *  buffer re-serializes off the same doc. */
  function pasteFromPane(f: MarcField): void {
    const rec = records[active];
    if (rec) records[active] = { ...rec, fields: [...rec.fields, f] };
  }

  onMount(() => void load());

  async function load(): Promise<void> {
    loading = true;
    error = "";
    try {
      const res = await fetchMarc(workId);
      etag = res.etag;
      records = res.records ?? [];
      knownLoss = res.knownLoss ?? {};
      active = Math.min(active, Math.max(0, records.length - 1));
      textValid = true;
    } catch (e) {
      error = e instanceof ApiError ? e.message : "MARC load failed";
    } finally {
      loading = false;
    }
  }

  async function preview(): Promise<void> {
    busy = true;
    status = "";
    error = "";
    try {
      const res = await postMarc(workId, active, $state.snapshot(records[active]), { dryRun: true });
      diff = res.diff;
      if (res.diff.added.length === 0 && res.diff.removed.length === 0) {
        status = "no changes -- saving would be a no-op";
      }
    } catch (e) {
      error = e instanceof ApiError ? e.message : "preview failed";
    } finally {
      busy = false;
    }
  }

  // sandboxSave renders the edit as if committed and never writes, matching
  // editor.ts's sandboxSave for the ops path. A page refresh restores the
  // pristine record.
  async function sandboxSave(): Promise<void> {
    busy = true;
    status = "";
    error = "";
    try {
      const res = await postMarc(workId, active, $state.snapshot(records[active]), { dryRun: true });
      diff = res.diff;
      status =
        res.diff.added.length + res.diff.removed.length === 0
          ? "nothing to save -- the record is untouched"
          : "rendered in the demo -- not saved";
    } catch (e) {
      error = e instanceof ApiError ? e.message : "save failed";
    } finally {
      busy = false;
    }
  }

  async function save(): Promise<void> {
    busy = true;
    status = "";
    error = "";
    try {
      const res = await postMarc(workId, active, $state.snapshot(records[active]), { ifMatch: etag });
      etag = res.etag;
      diff = null;
      status =
        res.diff.added.length + res.diff.removed.length === 0
          ? "nothing to save -- the record is untouched"
          : `saved -- ${res.diff.added.length} added / ${res.diff.removed.length} removed quads`;
      await load();
    } catch (e) {
      if (e instanceof ConflictError) {
        error = "this record changed underneath you -- reloading the fresh state";
        await load();
      } else {
        error = e instanceof ApiError ? e.message : "save failed";
      }
    } finally {
      busy = false;
    }
  }
</script>

{#if loading}
  <p class="muted" aria-live="polite">Loading MARC…</p>
{:else if error && records.length === 0}
  <p class="error" role="alert">{error}</p>
{:else if records.length === 0}
  <p class="muted">This work decodes to no MARC records.</p>
{:else}
  {#if records.length > 1}
    <div class="tabs" role="group" aria-label="Record">
      {#each records as r, i (r.node)}
        <button class="tab" class:active={i === active} aria-pressed={i === active}
          onclick={() => ((active = i), (diff = null), (textValid = true))}>
          Record {i + 1}
        </button>
      {/each}
    </div>
  {/if}

  <p class="muted head">
    record node <code>{records[active].node}</code> · etag <code>{etag.slice(0, 12)}…</code>
    <span class="modes" role="group" aria-label="Editing surface">
      <button class="tab mini" class:active={mode === "grid"} aria-pressed={mode === "grid"} disabled={blocked}
        title={blocked ? "fix the text errors first" : ""} onclick={() => (mode = "grid")}>Grid</button>
      <button class="tab mini" class:active={mode === "text"} aria-pressed={mode === "text"}
        onclick={() => ((mode = "text"), (textValid = true))}>Text</button>
    </span>
  </p>
  <p aria-live="polite">
    {#if status}<span class="ok">{status}</span>{/if}
    {#if error}<span class="error">{error}</span>{/if}
  </p>

  <FieldClipboardPane onpaste={pasteFromPane} />

  {#key records[active].node}
    {#if mode === "grid"}
      <MarcGrid record={records[active]} {knownLoss} {scope} onchange={(r) => (records[active] = r)} />
    {:else}
      <MarcTextEditor
        record={records[active]}
        {knownLoss}
        {scope}
        onchange={(r) => (records[active] = r)}
        onvalid={(ok) => (textValid = ok)}
      />
    {/if}
  {/key}

  {#if diff}
    <DiffPreview {diff} onclose={() => (diff = null)} />
  {/if}

  <p class="actions">
    <button class="button button--quiet" onclick={() => void preview()} disabled={busy || blocked}>Preview delta</button>
    {#if sandbox}
      <button class="button" onclick={() => void sandboxSave()} disabled={busy || blocked} title="Renders the edit in the demo; not saved"
        >{busy ? "Working…" : "Save MARC (demo)"}</button
      >
    {:else if !readOnly}
      <button class="button" onclick={() => void save()} disabled={busy || blocked}>{busy ? "Working…" : "Save MARC"}</button>
    {/if}
    <button class="button button--quiet" onclick={() => void load()} disabled={busy}>Discard edits</button>
    {#if blocked}<span class="error">the text buffer has parse errors -- saving is blocked</span>{/if}
    {#if readOnly && !sandbox}<span class="muted">read-only demo -- Preview delta shows the change without saving</span>{/if}
  </p>
{/if}

<style>
  .tabs {
    display: flex;
    gap: 0.4rem;
    margin: 0.5rem 0;
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
  .head {
    font-size: 0.8rem;
    display: flex;
    gap: 0.5rem;
    align-items: baseline;
    flex-wrap: wrap;
  }
  .modes {
    margin-left: auto;
    display: inline-flex;
    gap: 0.3rem;
  }
  .tab.mini {
    font-size: 0.72rem;
    padding: 0.1em 0.7em;
  }
  .actions {
    display: flex;
    gap: 0.75rem;
    margin-top: 0.9rem;
  }
  .ok {
    color: var(--accent);
  }
</style>
