<script lang="ts">
  // The withdrawal review queue: feed-only works the last
  // reconciliation flagged as gone from their feed. One-key triage --
  // s suppresses (hidden from projection, flag kept as the reason),
  // p keeps (flag cleared, decision pinned so reconciliation never
  // re-flags), o opens the work.
  import { onDestroy, onMount } from "svelte";
  import { decideWithdrawn, fetchWithdrawn, humanApiMessage } from "../lib/api";
  import { bindKeys, popScope, pushScope } from "../lib/keyboard";
  import { navigate } from "../lib/router";
  import { screenState } from "../lib/screenState.svelte";
  import RowList from "../components/RowList.svelte";
  import type { WorkSummary } from "../lib/types";

  const SCOPE = "withdrawals";

  const st = screenState("withdrawals", () => ({
    works: [] as WorkSummary[],
    selected: 0,
  }));

  let loading = $state(true);
  let status = $state("");
  let error = $state("");

  pushScope(SCOPE);
  onDestroy(() => popScope(SCOPE));

  onMount(() => {
    const unbind = bindKeys(SCOPE, {
      s: { description: "suppress the selected work (hide from the public catalog)", legend: "suppress", handler: () => void decide("suppress") },
      p: { description: "keep the selected work despite the withdrawal", legend: "keep", handler: () => void decide("keep") },
      o: { description: "open the selected work", legend: "open", handler: openSelected },
    });
    void load();
    return unbind;
  });

  async function load(): Promise<void> {
    loading = true;
    error = "";
    try {
      st.works = (await fetchWithdrawn()).works ?? [];
      st.selected = Math.min(st.selected, Math.max(0, st.works.length - 1));
    } catch (e) {
      error = humanApiMessage(e, "loading the queue failed");
    } finally {
      loading = false;
    }
  }

  async function decide(action: "keep" | "suppress", target?: WorkSummary): Promise<void> {
    const w = target ?? st.works[st.selected];
    if (!w) return;
    error = "";
    try {
      await decideWithdrawn(w.WorkID, action);
      status = `${w.Title || w.WorkID}: ${action === "keep" ? "kept" : "suppressed"}`;
      st.works = st.works.filter((x) => x.WorkID !== w.WorkID);
      st.selected = Math.min(st.selected, Math.max(0, st.works.length - 1));
    } catch (e) {
      error = humanApiMessage(e, "the decision failed");
    }
  }

  function openSelected(): void {
    const w = st.works[st.selected];
    if (w) navigate(`/works/${encodeURIComponent(w.WorkID)}`);
  }
</script>

<main class="wide" id="main" tabindex="-1">
  <h1>Withdrawals</h1>
  <p class="lede muted">
    Works whose only bib source stopped listing them (feed reconciliation). Suppress hides a work from the public
    catalog; keep pins it so reconciliation never re-flags it. Nothing is deleted either way.
  </p>
  <p aria-live="polite" class="status">
    {#if loading}<span class="muted">Loading…</span>{/if}
    {#if status}<span class="ok">{status}</span>{/if}
    {#if error}<span class="error">{error}</span>{/if}
  </p>

  {#if !loading && st.works.length === 0}
    <p class="muted">Nothing awaiting review. Withdrawals appear after an ingest run with --reconcile.</p>
  {:else}
    <RowList items={st.works} bind:selected={st.selected} getKey={(w) => w.WorkID} ariaLabel="Withdrawn works" scope={SCOPE} itemName="work" onactivate={openSelected}>
      {#snippet row(w: WorkSummary)}
        <div class="rrow">
          <span class="title">{w.Title || "(untitled)"}</span>
          <span class="muted who">{w.Contributors?.join("; ") ?? ""}</span>
          <span class="date" title="flagged by the feed reconciliation">since {w.Withdrawn}</span>
          <span class="id">{w.WorkID}</span>
          <span class="acts">
            <button class="button button--quiet mini" onclick={() => void decide("suppress", w)}>Suppress</button>
            <button class="button button--quiet mini" onclick={() => void decide("keep", w)}>Keep</button>
          </span>
        </div>
      {/snippet}
    </RowList>
  {/if}
</main>

<style>
  .lede {
    margin: 0.2rem 0 0.6rem;
    max-width: 46rem;
  }
  .status {
    min-height: 1.2em;
    font-size: 0.85rem;
    margin: 0.3rem 0;
  }
  .rrow {
    display: grid;
    grid-template-columns: minmax(12rem, 1.3fr) 1fr auto auto auto;
    gap: 0 0.9rem;
    align-items: baseline;
    padding: 0.22rem 0.55rem;
  }
  .title {
    font-weight: 600;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .who {
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .date {
    font-size: var(--fs-meta);
    color: #c77d0a;
    white-space: nowrap;
  }
  .id {
    font-family: var(--mono);
    font-size: var(--fs-meta);
    color: var(--ink-muted);
  }
  .acts {
    display: inline-flex;
    gap: 0.3rem;
  }
  .mini {
    font-size: 0.72rem;
    padding: 0.05em 0.6em;
  }
  .ok {
    color: var(--accent);
  }
</style>
