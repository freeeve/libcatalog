<script lang="ts">
  // Audit-log reader (tasks/299): the picked month's entries with NO workId
  // filter, so the system-level actions -- user/role changes, profile edits,
  // imports, batch-run summaries -- that carry no work and are therefore
  // invisible in a work's History tab finally have a screen. The server-side
  // per-work filter is deliberately correct and stays; this is the unfiltered
  // view it never had a reader for. Librarian-gated, like GET /v1/audit.
  import { onMount } from "svelte";
  import { ApiError, fetchAudit, humanApiMessage } from "../lib/api";
  import type { AuditEntry } from "../lib/types";

  let {
    initialMonth = "",
    initialActor = "",
    initialAction = "",
  }: { initialMonth?: string; initialActor?: string; initialAction?: string } = $props();

  function currentMonth(): string {
    return new Date().toISOString().slice(0, 7);
  }

  // svelte-ignore state_referenced_locally
  let month = $state(initialMonth || currentMonth());
  // svelte-ignore state_referenced_locally
  let actor = $state(initialActor);
  // svelte-ignore state_referenced_locally
  let action = $state(initialAction);
  let entries = $state<AuditEntry[]>([]);
  let loading = $state(false);
  let error = $state("");

  // The filter choices are the values actually present this month, so an actor
  // or action option never promises rows the month does not hold.
  const actors = $derived([...new Set(entries.map((e) => e.actor))].sort());
  const actions = $derived([...new Set(entries.map((e) => e.action))].sort());
  const shown = $derived(entries.filter((e) => (!actor || e.actor === actor) && (!action || e.action === action)));

  onMount(() => void load());

  async function load(): Promise<void> {
    loading = true;
    error = "";
    try {
      const res = await fetchAudit(month); // no workId: the whole month
      entries = res.entries ?? [];
      // Drop a filter the new month cannot honour, so switching months never
      // leaves the list mysteriously empty under a stale selection.
      if (actor && !entries.some((e) => e.actor === actor)) actor = "";
      if (action && !entries.some((e) => e.action === action)) action = "";
    } catch (e) {
      entries = [];
      error = e instanceof ApiError ? `audit load failed: ${humanApiMessage(e, "the request was rejected")}` : "audit load failed";
    } finally {
      loading = false;
    }
  }

  /** Renders a note for display. A batch run's note is a RunNote JSON blob
   *  (tasks/239); unpack the fields that describe what the run did rather than
   *  showing raw JSON. Plain-string notes pass through untouched. */
  function formatNote(note: string): string {
    if (!note.startsWith("{")) return note;
    try {
      const n = JSON.parse(note) as Record<string, unknown>;
      const parts: string[] = [];
      if (typeof n.selection === "string" && n.selection) parts.push(n.selection);
      for (const key of ["matched", "applied", "rewritten", "failed", "added", "removed"]) {
        if (typeof n[key] === "number") parts.push(`${key} ${n[key] as number}`);
      }
      if (Array.isArray(n.works)) parts.push(`${n.works.length} work${n.works.length === 1 ? "" : "s"}`);
      return parts.length ? parts.join(" · ") : note;
    } catch {
      return note;
    }
  }
</script>

<main class="wide" id="main" tabindex="-1">
  <h1>Audit log</h1>
  <p class="muted lede">
    Every recorded action for the month, including the system-level ones -- user and role changes, editing-profile
    edits, imports, batch runs -- that no single work's History tab shows.
  </p>

  <form
    class="filters"
    onsubmit={(ev) => {
      ev.preventDefault();
      void load();
    }}
  >
    <label>
      <span class="muted">Month</span>
      <input type="month" bind:value={month} max={currentMonth()} onchange={() => void load()} />
    </label>
    <label>
      <span class="muted">Actor</span>
      <select bind:value={actor}>
        <option value="">all actors</option>
        {#each actors as a (a)}<option value={a}>{a}</option>{/each}
      </select>
    </label>
    <label>
      <span class="muted">Action</span>
      <select bind:value={action}>
        <option value="">all actions</option>
        {#each actions as a (a)}<option value={a}>{a}</option>{/each}
      </select>
    </label>
  </form>

  <p class="muted count" aria-live="polite">
    {#if loading}
      Loading…
    {:else if error}
      <span class="error">{error}</span>
    {:else if actor || action}
      Showing {shown.length} of {entries.length} entr{entries.length === 1 ? "y" : "ies"} in {month}
    {:else}
      {entries.length} entr{entries.length === 1 ? "y" : "ies"} in {month}
    {/if}
  </p>

  {#if !loading && !error}
    <ul class="entries">
      {#each shown as e, i (e.at + i)}
        <li>
          <span class="action">{e.action}</span>
          <span class="actor">{e.actor}</span>
          <span class="when muted">{new Date(e.at).toLocaleString()}</span>
          {#if e.workId}<a class="work mono" href={"#/works/" + encodeURIComponent(e.workId)}>{e.workId}</a>{/if}
          {#if e.note}<span class="note muted">{formatNote(e.note)}</span>{/if}
        </li>
      {:else}
        <li class="muted empty">No entries{actor || action ? " match the filter" : ""} in {month}.</li>
      {/each}
    </ul>
  {/if}
</main>

<style>
  .lede {
    max-width: 60ch;
  }
  .filters {
    display: flex;
    flex-wrap: wrap;
    gap: 1rem;
    align-items: end;
    margin: 0.8rem 0;
  }
  .filters label {
    display: flex;
    flex-direction: column;
    gap: 0.2rem;
    font-size: 0.85rem;
  }
  .filters input,
  .filters select {
    font: inherit;
    color: var(--ink);
    background: var(--bg);
    border: 1px solid var(--rule);
    border-radius: 4px;
    padding: 0.3em 0.4em;
  }
  .count {
    margin: 0.4rem 0;
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
  .mono {
    font-family: var(--mono);
  }
  .work {
    font-size: 0.8rem;
    color: var(--accent);
  }
  .when,
  .note {
    font-size: 0.85rem;
  }
  .note {
    flex-basis: 100%;
  }
</style>
