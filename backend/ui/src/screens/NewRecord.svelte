<script lang="ts">
  // Original cataloging (tasks/077): a MARC draft born in the editor -- pick
  // a material-type skeleton, edit in the grid or the text surface (the same
  // components the work editor hosts), and stage it for review. Staging runs
  // the minimum-viability gate server-side; refusals come back anchored to
  // their fields. The draft lives in screenState only -- nothing touches the
  // grain tree until the staged batch commits.
  import { onDestroy, onMount } from "svelte";
  import { FieldedApiError, fetchCopycatTemplates, humanApiMessage, stageOriginalRecord } from "../lib/api";
  import { bindKeys, popScope, pushScope } from "../lib/keyboard";
  import { navigate } from "../lib/router";
  import { screenState } from "../lib/screenState.svelte";
  import FieldClipboardPane from "../components/FieldClipboardPane.svelte";
  import MarcGrid from "../components/MarcGrid.svelte";
  import MarcTextEditor from "../components/MarcTextEditor.svelte";
  import type { CopycatTemplate, MarcField, MarcFieldError, MarcRecordDoc } from "../lib/types";

  const SCOPE = "new-record";

  const st = screenState("new-record", () => ({
    templateId: "",
    label: "",
    record: null as MarcRecordDoc | null,
    mode: "grid" as "grid" | "text",
  }));

  let templates = $state<CopycatTemplate[]>([]);
  let fieldErrors = $state<MarcFieldError[]>([]);
  let textValid = $state(true);
  let busy = $state(false);
  let error = $state("");

  const blocked = $derived(st.mode === "text" && !textValid);

  pushScope(SCOPE);
  onDestroy(() => popScope(SCOPE));

  onMount(() => {
    const unbind = bindKeys(SCOPE, {
      "mod+s": { description: "stage the draft for review", legend: "stage", handler: () => void stage() },
    });
    void loadTemplates();
    return unbind;
  });

  async function loadTemplates(): Promise<void> {
    try {
      templates = (await fetchCopycatTemplates()).templates ?? [];
    } catch {
      // Only the fetch belongs to this message: an error thrown after a
      // 200 must not masquerade as a load failure (tasks/224).
      error = "loading the templates failed";
      return;
    }
    if (!st.record && templates.length > 0) pick(st.templateId || templates[0].id);
  }

  /** Starts (or restarts) the draft from a skeleton. Snapshot before the
   *  clone: templates is $state, and structuredClone cannot clone its
   *  proxies (tasks/224). */
  function pick(id: string): void {
    const tpl = templates.find((t) => t.id === id);
    if (!tpl) return;
    st.templateId = id;
    st.record = structuredClone($state.snapshot(tpl.record)) as MarcRecordDoc;
    fieldErrors = [];
    textValid = true;
  }

  function pasteFromPane(f: MarcField): void {
    if (st.record) st.record = { ...st.record, fields: [...st.record.fields, f] };
  }

  async function stage(): Promise<void> {
    if (!st.record || blocked || busy) return;
    busy = true;
    error = "";
    fieldErrors = [];
    try {
      const res = await stageOriginalRecord(st.label.trim(), $state.snapshot(st.record));
      st.record = null;
      st.label = "";
      navigate(`/copycat?batch=${encodeURIComponent(res.batch.id)}`);
    } catch (e) {
      if (e instanceof FieldedApiError) {
        fieldErrors = e.fields;
        error = e.message;
      } else {
        error = humanApiMessage(e, "staging failed");
      }
    } finally {
      busy = false;
    }
  }
</script>

<main class="wide" id="main" tabindex="-1">
  <p class="back"><a href="#/copycat">← Back to import</a></p>
  <h1>New record</h1>

  <div class="row">
    <label class="muted" for="nr-template">Template</label>
    <select id="nr-template" value={st.templateId} onchange={(ev) => pick((ev.currentTarget as HTMLSelectElement).value)}>
      {#each templates as t (t.id)}
        <option value={t.id}>{t.label}</option>
      {/each}
    </select>
    <input class="grow" aria-label="Batch label" bind:value={st.label} placeholder="batch label (defaults to the title)" />
    <span class="modes" role="group" aria-label="Editing surface">
      <button class="button button--quiet mini" class:on={st.mode === "grid"} aria-pressed={st.mode === "grid"}
        disabled={blocked} onclick={() => (st.mode = "grid")}>Grid</button>
      <button class="button button--quiet mini" class:on={st.mode === "text"} aria-pressed={st.mode === "text"}
        onclick={() => ((st.mode = "text"), (textValid = true))}>Text</button>
    </span>
  </div>
  <p class="muted hint">
    Picking a template restarts the draft. Empty skeleton rows vanish at staging; the draft stages into a normal
    review batch (source "original") and enters the catalog only on commit.
  </p>

  <p aria-live="polite">
    {#if error}<span class="error">{error}</span>{/if}
  </p>
  {#if fieldErrors.length > 0}
    <ul class="ferrs" role="alert">
      {#each fieldErrors as fe (fe.tag + fe.message)}
        <li><span class="mono">{fe.tag}</span> -- {fe.message}</li>
      {/each}
    </ul>
  {/if}

  {#if st.record}
    <FieldClipboardPane onpaste={pasteFromPane} />
    {#key st.templateId + st.mode}
      {#if st.mode === "grid"}
        <MarcGrid record={st.record} scope={SCOPE} onchange={(r) => (st.record = r)} />
      {:else}
        <MarcTextEditor record={st.record} scope={SCOPE} onchange={(r) => (st.record = r)} onvalid={(ok) => (textValid = ok)} />
      {/if}
    {/key}
    <p class="actions">
      <button class="button" onclick={() => void stage()} disabled={busy || blocked}>
        {busy ? "Working…" : "Stage for review"}
      </button>
      <button class="button button--quiet" onclick={() => pick(st.templateId)} disabled={busy}>Restart from template</button>
      {#if blocked}<span class="error">the text buffer has parse errors -- staging is blocked</span>{/if}
    </p>
  {/if}
</main>

<style>
  .row {
    display: flex;
    gap: 0.5rem;
    align-items: center;
    flex-wrap: wrap;
    margin: 0.4rem 0;
  }
  .grow {
    flex: 1;
    min-width: 14rem;
    max-width: 26rem;
  }
  .modes {
    display: inline-flex;
    gap: 0.3rem;
    margin-left: auto;
  }
  .mini {
    font-size: 0.72rem;
    padding: 0.1em 0.7em;
  }
  .mini.on {
    background: var(--accent);
    border-color: var(--accent);
    color: var(--accent-ink);
  }
  .hint {
    font-size: 0.78rem;
    margin: 0.2rem 0 0.6rem;
  }
  .ferrs {
    margin: 0.3rem 0;
    padding-left: 1.1rem;
    font-size: 0.85rem;
    color: var(--danger);
  }
  .mono {
    font-family: var(--mono);
  }
  .actions {
    display: flex;
    gap: 0.75rem;
    align-items: center;
    margin-top: 0.9rem;
  }
  .back {
    margin: 0.2rem 0;
  }
</style>
