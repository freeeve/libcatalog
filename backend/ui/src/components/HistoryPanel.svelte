<script lang="ts">
  // Audit trail for one work: the picked month's entries (current month by
  // default, the month input goes back), action/actor/time/note per row.
  import { onMount } from "svelte";
  import { ApiError, fetchAudit } from "../lib/api";
  import type { AuditEntry } from "../lib/types";

  let { workId }: { workId: string } = $props();

  function currentMonth(): string {
    return new Date().toISOString().slice(0, 7);
  }

  let month = $state(currentMonth());
  let entries = $state<AuditEntry[]>([]);
  let loading = $state(false);
  let error = $state("");

  onMount(() => {
    void load();
  });

  async function load(): Promise<void> {
    loading = true;
    error = "";
    try {
      const res = await fetchAudit(month, workId);
      entries = res.entries ?? [];
    } catch (e) {
      entries = [];
      error = e instanceof ApiError ? `history load failed: ${e.message}` : "history load failed";
    } finally {
      loading = false;
    }
  }
</script>

<section class="history" aria-label="Edit history">
  <form
    class="pick"
    onsubmit={(ev) => {
      ev.preventDefault();
      void load();
    }}
  >
    <label for="hist-month">Month</label>
    <input id="hist-month" type="month" bind:value={month} max={currentMonth()} onchange={() => void load()} />
  </form>

  <p class="muted" aria-live="polite">
    {#if loading}
      Loading…
    {:else if error}
      <span class="error">{error}</span>
    {:else}
      {entries.length} entr{entries.length === 1 ? "y" : "ies"} in {month}
    {/if}
  </p>

  <ul class="entries">
    {#each entries as e, i (e.at + i)}
      <li>
        <span class="action">{e.action}</span>
        <span class="actor">{e.actor}</span>
        <span class="when muted">{new Date(e.at).toLocaleString()}</span>
        {#if e.note}<span class="note muted">{e.note}</span>{/if}
      </li>
    {/each}
  </ul>
</section>

<style>
  .pick {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    margin: 0.5rem 0;
  }
  .pick label {
    font-size: 0.85rem;
    font-weight: 600;
    color: var(--ink-muted);
  }
  .pick input {
    font: inherit;
    color: var(--ink);
    background: var(--bg);
    border: 1px solid var(--rule);
    border-radius: 4px;
    padding: 0.3em 0.4em;
  }
  .entries {
    list-style: none;
    margin: 0.25rem 0;
    padding: 0;
  }
  .entries li {
    display: flex;
    gap: 0.8rem;
    flex-wrap: wrap;
    align-items: baseline;
    border-bottom: 1px solid var(--rule);
    padding: 0.45rem 0.2rem;
  }
  .action {
    font-family: var(--mono);
    font-size: 0.8rem;
    font-weight: 700;
  }
  .actor {
    font-weight: 600;
  }
  .when,
  .note {
    font-size: 0.85rem;
  }
</style>
