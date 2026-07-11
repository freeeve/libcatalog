<script lang="ts">
  // External-search results as a keyboard triage list (scope "copycat"):
  // j/k move, x or Space toggles the pick, a picks all/none, v toggles the
  // selected result's full MARC, Enter stages the picked
  // records for review. Checkboxes stay for pointer users.
  import { onMount } from "svelte";
  import { isReadOnly } from "../lib/config";
  import { bindKeys } from "../lib/keyboard";
  import MarcRecordView from "./MarcRecordView.svelte";
  import RowList from "./RowList.svelte";
  import type { CopycatSearchResult } from "../lib/types";

  let {
    results,
    picked = $bindable({}),
    selected = $bindable(0),
    busy,
    onstage,
  }: {
    results: CopycatSearchResult[];
    picked?: Record<number, boolean>;
    selected?: number;
    busy: boolean;
    onstage: () => void;
  } = $props();

  const SCOPE = "copycat";
  const readOnly = isReadOnly();

  const pickedCount = $derived(Object.values(picked).filter(Boolean).length);

  let viewing = $state(false);

  onMount(() =>
    bindKeys(SCOPE, {
      x: { description: "pick or unpick the selected result", legend: "pick", handler: toggle },
      " ": { description: "pick or unpick the selected result", hidden: true, handler: toggle },
      a: { description: "pick all results (or none)", legend: "all/none", handler: toggleAll },
      v: { description: "show or hide the selected result's MARC", legend: "view marc", handler: () => (viewing = !viewing) },
      Enter: { description: "stage the picked records for review", legend: "stage", handler: () => !readOnly && pickedCount > 0 && onstage() },
    }),
  );

  function toggle(): void {
    if (results.length === 0) return;
    picked[selected] = !picked[selected];
  }

  function toggleAll(): void {
    const all = results.length > 0 && results.every((_, i) => picked[i]);
    const next: Record<number, boolean> = {};
    if (!all) results.forEach((_, i) => (next[i] = true));
    picked = next;
  }
</script>

<RowList items={results} bind:selected getKey={(_, i) => i} ariaLabel="External search results" scope={SCOPE} itemName="result">
  {#snippet row(r: CopycatSearchResult, i: number)}
    <div class="rrow" class:picked={picked[i]}>
      <input type="checkbox" aria-label={"Select " + (r.title || "result")} bind:checked={picked[i]} />
      <span class="mono target">{r.target}</span>
      <span class="title">{r.title}</span>
      <span class="muted author">{r.author}</span>
      <span class="muted edition">{r.edition ?? ""}</span>
      <span class="muted date">{r.date}</span>
      <span class="mono isbn">{r.isbn}</span>
      <span class="mono lccn">{r.lccn ?? ""}</span>
      <button
        class="button button--quiet mini"
        aria-expanded={viewing && selected === i}
        onclick={() => {
          const wasOpen = viewing && selected === i;
          selected = i;
          viewing = !wasOpen;
        }}
      >
        MARC
      </button>
    </div>
  {/snippet}
</RowList>
{#if viewing && results[selected]}
  <div class="preview" aria-label="MARC of the selected result">
    <p class="phead muted">
      {results[selected].target} · {results[selected].title || "(untitled)"}
      <button class="button button--quiet mini" onclick={() => (viewing = false)}>Close</button>
    </p>
    <MarcRecordView record={results[selected].record} />
  </div>
{/if}
{#if !readOnly}
  <p>
    <button class="button" onclick={onstage} disabled={busy || pickedCount === 0}>
      Stage {pickedCount || ""} selected for review
    </button>
  </p>
{/if}

<style>
  .rrow {
    display: grid;
    grid-template-columns: 1.4rem auto minmax(12rem, 1.4fr) minmax(7rem, 1fr) minmax(0, auto) auto auto auto auto;
    gap: 0 0.55rem;
    align-items: baseline;
    padding: 0.2rem 0.55rem;
  }
  .rrow.picked {
    background: var(--tint-ok);
  }
  .rrow input {
    align-self: center;
  }
  .mono {
    font-family: var(--mono);
    font-size: var(--fs-meta);
    color: var(--ink-muted);
  }
  .title {
    font-weight: 600;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .author,
  .edition {
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .edition,
  .date {
    font-size: var(--fs-meta);
  }
  .mini {
    font-size: 0.72rem;
    padding: 0.05em 0.6em;
  }
  .preview {
    border: 1px solid var(--rule);
    border-radius: 8px;
    padding: 0.4rem 0.7rem 0.6rem;
    margin: 0.5rem 0;
    max-height: 50vh;
    overflow-y: auto;
  }
  .phead {
    display: flex;
    gap: 0.6rem;
    align-items: baseline;
    justify-content: space-between;
    font-size: 0.8rem;
    margin: 0.1rem 0 0.4rem;
  }
</style>
