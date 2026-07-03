<script lang="ts">
  // External-search results as a keyboard triage list (scope "copycat"):
  // j/k move, x or Space toggles the pick, a picks all/none, Enter stages
  // the picked records for review. Checkboxes stay for pointer users.
  import { onMount } from "svelte";
  import { bindKeys } from "../lib/keyboard";
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

  const pickedCount = $derived(Object.values(picked).filter(Boolean).length);

  onMount(() =>
    bindKeys(SCOPE, {
      x: { description: "pick or unpick the selected result", legend: "pick", handler: toggle },
      " ": { description: "pick or unpick the selected result", hidden: true, handler: toggle },
      a: { description: "pick all results (or none)", legend: "all/none", handler: toggleAll },
      Enter: { description: "stage the picked records for review", legend: "stage", handler: () => pickedCount > 0 && onstage() },
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
      <span class="muted date">{r.date}</span>
      <span class="mono isbn">{r.isbn}</span>
    </div>
  {/snippet}
</RowList>
<p>
  <button class="button" onclick={onstage} disabled={busy || pickedCount === 0}>
    Stage {pickedCount || ""} selected for review
  </button>
</p>

<style>
  .rrow {
    display: grid;
    grid-template-columns: 1.4rem auto minmax(12rem, 1.4fr) minmax(8rem, 1fr) auto auto;
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
  .author {
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .date {
    font-size: var(--fs-meta);
  }
</style>
