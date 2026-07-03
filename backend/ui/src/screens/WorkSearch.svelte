<script lang="ts">
  // Debounced search over /v1/works with keyboard-navigable results:
  // RowList carries j/k/arrows and Enter-to-open; "/" refocuses the box.
  import { onMount } from "svelte";
  import { fetchWorks, ApiError } from "../lib/api";
  import { bindKeys, pushScope, popScope } from "../lib/keyboard";
  import { navigate } from "../lib/router";
  import RowList from "../components/RowList.svelte";
  import type { WorkSummary } from "../lib/types";

  const SCOPE = "works";
  const DEBOUNCE_MS = 250;

  let q = $state("");
  let works = $state<WorkSummary[]>([]);
  let total = $state(0);
  let selected = $state(0);
  let error = $state("");
  let loading = $state(false);
  let timer: ReturnType<typeof setTimeout> | undefined;

  onMount(() => {
    pushScope(SCOPE);
    const unbind = bindKeys(SCOPE, {
      "/": { description: "focus the search box", legend: "search", handler: focusSearch },
    });
    void search("");
    return () => {
      unbind();
      popScope(SCOPE);
      clearTimeout(timer);
    };
  });

  function onInput(): void {
    clearTimeout(timer);
    timer = setTimeout(() => void search(q), DEBOUNCE_MS);
  }

  async function search(query: string): Promise<void> {
    loading = true;
    error = "";
    try {
      const page = await fetchWorks(query);
      works = page.works ?? [];
      total = page.total;
      selected = 0;
    } catch (e) {
      works = [];
      error = e instanceof ApiError && e.status === 401 ? "session expired -- sign in again" : "search failed";
    } finally {
      loading = false;
    }
  }

  function open(w: WorkSummary): void {
    navigate(`/works/${encodeURIComponent(w.WorkID)}`);
  }

  function focusSearch(): void {
    document.getElementById("work-q")?.focus();
  }
</script>

<main>
  <h1>Work search</h1>
  <p>
    <label for="work-q" class="muted">Title, contributor, tag, ISBN, or id</label>
  </p>
  <input id="work-q" type="search" bind:value={q} oninput={onInput} placeholder="Search works…" autocomplete="off" />
  <p class="muted" aria-live="polite">
    {#if loading}Searching…{:else if error}<span class="error">{error}</span>{:else}{works.length} shown of {total} works{/if}
    {#if !loading && !error && works.length > 0}
      · <a href={q.trim() ? "#/exports?kind=search&q=" + encodeURIComponent(q.trim()) : "#/exports?kind=all"}>Export these results…</a>
    {/if}
  </p>

  <RowList items={works} bind:selected getKey={(w) => w.WorkID} ariaLabel="Search results" scope={SCOPE} itemName="result" onactivate={open}>
    {#snippet row(w: WorkSummary)}
      <a class="row-link" href={"#/works/" + encodeURIComponent(w.WorkID)}>
        <span class="title">{w.Title || "(untitled)"}</span>
        {#if w.Contributors?.length}
          <span class="muted">{w.Contributors.join("; ")}</span>
        {/if}
        {#if w.Tags?.length}
          <span class="tags">{w.Tags.join(", ")}</span>
        {/if}
        <span class="id">{w.WorkID}</span>
      </a>
    {/snippet}
  </RowList>
</main>

<style>
  #work-q {
    width: 100%;
    max-width: 28rem;
    font-size: 1rem;
  }
  .row-link {
    display: grid;
    grid-template-columns: 1fr auto;
    gap: 0.1rem 0.9rem;
    padding: 0.5rem 0.6rem;
    text-decoration: none;
    color: inherit;
  }
  .title {
    font-weight: 600;
    color: var(--accent);
  }
  .id {
    font-family: var(--mono);
    font-size: 0.78rem;
    color: var(--ink-muted);
    grid-column: 2;
    grid-row: 1;
  }
  .tags {
    font-size: 0.85rem;
    color: var(--ink-muted);
  }
</style>
