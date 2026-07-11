<script lang="ts">
  // Visibility stance: suppress hides a work from projection
  // (restorable, no redirect); tombstone retires it with an optional
  // successor redirect. Never row-deletion.
  import { onMount } from "svelte";
  import { fetchVisibility, setVisibility, ApiError } from "../lib/api";
  import { isReadOnly } from "../lib/config";
  import type { WorkVisibility } from "../lib/types";

  let { workId }: { workId: string } = $props();

  const readOnly = isReadOnly();

  let vis = $state<WorkVisibility | null>(null);
  let redirectTo = $state("");
  let tombstoning = $state(false);
  let error = $state("");

  onMount(() => void load());

  async function load(): Promise<void> {
    try {
      vis = await fetchVisibility(workId);
      redirectTo = vis.redirectTo ?? "";
    } catch {
      vis = null;
    }
  }

  async function act(action: "tombstone" | "untombstone" | "suppress" | "unsuppress"): Promise<void> {
    error = "";
    try {
      vis = await setVisibility(workId, action, action === "tombstone" ? redirectTo.trim() : undefined);
      tombstoning = false;
    } catch (e) {
      error = e instanceof ApiError ? e.message : "updating visibility failed";
    }
  }
</script>

{#if vis}
  <div class="vis" role="group" aria-label="Visibility">
    {#if vis.tombstoned}
      <span class="badge warn">tombstoned{vis.redirectTo ? ` → ${vis.redirectTo}` : " (gone)"}</span>
      {#if !readOnly}
        <button class="button button--quiet mini" onclick={() => void act("untombstone")}>Restore</button>
      {/if}
    {:else if vis.suppressed}
      <span class="badge">suppressed</span>
      {#if !readOnly}
        <button class="button button--quiet mini" onclick={() => void act("unsuppress")}>Unsuppress</button>
      {/if}
    {:else if !readOnly}
      <button class="button button--quiet mini" onclick={() => void act("suppress")}>Suppress</button>
      {#if tombstoning}
        <input class="mono target" aria-label="Redirect to work id (optional)" bind:value={redirectTo} placeholder="redirect to w… (optional)" />
        <button class="button mini" onclick={() => void act("tombstone")}>Tombstone</button>
        <button class="button button--quiet mini" onclick={() => (tombstoning = false)}>Cancel</button>
      {:else}
        <button class="button button--quiet mini" onclick={() => (tombstoning = true)}>Tombstone…</button>
      {/if}
    {/if}
    {#if error}<span class="error">{error}</span>{/if}
  </div>
{/if}

<style>
  .vis {
    display: inline-flex;
    gap: 0.45rem;
    align-items: center;
    flex-wrap: wrap;
    font-size: 0.85rem;
  }
  .badge {
    font-size: 0.72rem;
    font-weight: 700;
    text-transform: uppercase;
    border: 1px solid var(--rule);
    border-radius: 999px;
    padding: 0.08em 0.7em;
    color: var(--ink-muted);
  }
  .badge.warn {
    border-color: var(--danger);
    color: var(--danger);
  }
  .mini {
    font-size: 0.75rem;
    padding: 0.08em 0.7em;
  }
  .mono {
    font-family: var(--mono);
  }
  .target {
    width: 13rem;
  }
</style>
