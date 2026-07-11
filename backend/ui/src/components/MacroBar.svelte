<script lang="ts">
  // Editor macro bar: record the currently staged ops as a
  // macro, or replay one against this record (parameter prompts inline).
  // Macros carrying a single-character shortcut key bind into the editor
  // scope while this bar is mounted.
  import { onMount } from "svelte";
  import { ApiError, createMacro, fetchMacros } from "../lib/api";
  import { isReadOnly } from "../lib/config";
  import { bindKeys } from "../lib/keyboard";
  import { applyParams, hasParams } from "../lib/macros";
  import type { Macro, Op } from "../lib/types";

  let {
    ops,
    onapply,
  }: {
    /** The staged op list (what recording captures). */
    ops: Op[];
    /** Stages a replayed op into the editing session. */
    onapply: (op: Op) => void;
  } = $props();

  let macros = $state<Macro[]>([]);
  let selectedId = $state("");
  let paramValues = $state<Record<string, string>>({});
  let recording = $state(false);
  let recordLabel = $state("");
  let recordShared = $state(false);
  let status = $state("");
  let error = $state("");
  let unbindKeys: (() => void) | null = null;

  const selected = $derived(macros.find((m) => m.id === selectedId) ?? null);

  onMount(() => {
    void load();
    return () => unbindKeys?.();
  });

  async function load(): Promise<void> {
    try {
      macros = (await fetchMacros()).macros ?? [];
    } catch {
      macros = [];
    }
    bindShortcuts();
  }

  /** Binds each single-character macro key into the editor scope. */
  function bindShortcuts(): void {
    unbindKeys?.();
    const map: Record<string, { description: string; handler: () => void }> = {};
    for (const m of macros) {
      if ((m.keys ?? "").length === 1) {
        map[m.keys!] = {
          description: `apply macro: ${m.label}`,
          handler: () => {
            selectedId = m.id;
            paramValues = {};
            if (!hasParams(m)) replay(m);
          },
        };
      }
    }
    // "macro:" namespaces these ids: a macro on "2" is "editor:macro:2", not
    // "editor:2", so bindKeys sees a genuine collision and drops it rather
    // than silently replacing the MARC-tab chord. Server-side
    // validation means a colliding macro should not exist; this is the guard
    // for the ones stored before that landed.
    unbindKeys = Object.keys(map).length > 0 ? bindKeys("editor", map, "macro:") : null;
  }

  function replay(m: Macro): void {
    status = "";
    error = "";
    try {
      for (const op of applyParams(m, paramValues)) onapply(op);
      status = `staged ${m.ops.length} edit${m.ops.length === 1 ? "" : "s"} from "${m.label}" -- preview before saving`;
      selectedId = "";
      paramValues = {};
    } catch (e) {
      error = e instanceof Error ? e.message : "macro replay failed";
    }
  }

  async function record(): Promise<void> {
    error = "";
    if (!recordLabel.trim() || ops.length === 0) return;
    try {
      await createMacro({ label: recordLabel.trim(), shared: recordShared, ops });
      status = `recorded "${recordLabel.trim()}" (${ops.length} edit${ops.length === 1 ? "" : "s"})`;
      recording = false;
      recordLabel = "";
      recordShared = false;
      await load();
    } catch (e) {
      error = e instanceof ApiError ? e.message : "recording failed";
    }
  }
</script>

<div class="macrobar" aria-label="Macros">
  <span class="lead muted">Macros:</span>

  <select aria-label="Apply a macro" bind:value={selectedId} onchange={() => (paramValues = {})}>
    <option value="">Apply macro…</option>
    {#each macros as m (m.id)}
      <option value={m.id}>{m.label}{m.keys ? ` (${m.keys})` : ""}{m.shared ? " · shared" : ""}</option>
    {/each}
  </select>

  {#if selected}
    {#each selected.params ?? [] as p (p.name)}
      <label class="param">
        <span>{p.label || p.name}</span>
        <input bind:value={paramValues[p.name]} placeholder={p.default || ""} aria-label={p.label || p.name} />
      </label>
    {/each}
    <button class="button" onclick={() => selected && replay(selected)}>Apply</button>
  {/if}

  {#if recording && !isReadOnly()}
    <input class="rec-label" bind:value={recordLabel} placeholder="Macro name" aria-label="Macro name" />
    <label class="param"><input type="checkbox" bind:checked={recordShared} /> shared</label>
    <button class="button" onclick={() => void record()} disabled={!recordLabel.trim()}>Save macro</button>
    <button class="button button--quiet" onclick={() => (recording = false)}>Cancel</button>
  {:else if !isReadOnly()}
    <button
      class="button button--quiet"
      onclick={() => (recording = true)}
      disabled={ops.length === 0}
      title={ops.length === 0 ? "stage some edits first" : ""}
    >
      Save staged edits as macro… ({ops.length})
    </button>
  {/if}

  <span aria-live="polite">
    {#if status}<span class="ok">{status}</span>{/if}
    {#if error}<span class="error">{error}</span>{/if}
  </span>
</div>

<style>
  .macrobar {
    display: flex;
    gap: 0.55rem;
    align-items: center;
    flex-wrap: wrap;
    border: 1px dashed var(--rule);
    border-radius: 6px;
    padding: 0.45rem 0.7rem;
    margin: 0.7rem 0;
    font-size: 0.9rem;
  }
  .lead {
    font-weight: 600;
  }
  .param {
    display: inline-flex;
    gap: 0.3rem;
    align-items: center;
    font-size: 0.85rem;
  }
  .param input:not([type="checkbox"]) {
    width: 9rem;
  }
  .rec-label {
    width: 12rem;
  }
  .ok {
    color: var(--accent);
  }
</style>
