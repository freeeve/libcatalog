<script lang="ts">
  // Debounced search over /v1/works with keyboard-navigable results:
  // ArrowUp/ArrowDown (or j/k) move the selection, Enter opens the work.
  import { onMount } from "svelte";
  import { fetchWorks, ApiError } from "../lib/api";
  import { bindKeys, pushScope, popScope } from "../lib/keyboard";
  import { navigate } from "../lib/router";
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
  let listEl = $state<HTMLElement | null>(null);

  onMount(() => {
    pushScope(SCOPE);
    const unbind = bindKeys(SCOPE, {
      ArrowDown: { description: "next result", handler: () => move(1) },
      ArrowUp: { description: "previous result", handler: () => move(-1) },
      j: { description: "next result", handler: () => move(1) },
      k: { description: "previous result", handler: () => move(-1) },
      Enter: { description: "open selected work", handler: open },
      "/": { description: "focus the search box", handler: focusSearch },
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

  function move(delta: number): void {
    if (works.length === 0) return;
    selected = Math.min(works.length - 1, Math.max(0, selected + delta));
    listEl?.querySelectorAll("li")[selected]?.scrollIntoView({ block: "nearest" });
  }

  function open(): void {
    const w = works[selected];
    if (w) navigate(`/works/${encodeURIComponent(w.WorkID)}`);
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
  </p>

  <ul class="results" bind:this={listEl} aria-label="Search results">
    {#each works as w, i (w.WorkID)}
      <li class:selected={i === selected}>
        <a href={"#/works/" + encodeURIComponent(w.WorkID)} onfocus={() => (selected = i)}>
          <span class="title">{w.Title || "(untitled)"}</span>
          {#if w.Contributors?.length}
            <span class="muted">{w.Contributors.join("; ")}</span>
          {/if}
          {#if w.Tags?.length}
            <span class="tags">{w.Tags.join(", ")}</span>
          {/if}
          <span class="id">{w.WorkID}</span>
        </a>
      </li>
    {/each}
  </ul>
</main>

<style>
  #work-q {
    width: 100%;
    max-width: 28rem;
    font-size: 1rem;
  }
  .results {
    list-style: none;
    padding: 0;
    margin: 0.5rem 0;
  }
  .results li {
    border: 1px solid transparent;
    border-bottom-color: var(--rule);
  }
  .results li.selected {
    border-color: var(--accent);
    border-radius: 4px;
    background: var(--surface);
  }
  .results a {
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
