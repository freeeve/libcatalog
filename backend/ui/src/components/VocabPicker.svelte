<script lang="ts">
  // Modal vocabulary picker: scheme tabs over config.schemes (folk excluded),
  // search-as-you-type against /v1/terms, arrow-key result navigation, and a
  // details pane (definition, alt labels, semantic neighborhood) for the
  // highlighted term. Modal owns the trap/Escape/focus restore; Enter or
  // click emits the term through onselect.
  import { onMount } from "svelte";
  import { searchTerms } from "../lib/api";
  import { getConfig } from "../lib/config";
  import { popScope, pushScope } from "../lib/keyboard";
  import { allAltLabels, bestDefinition, bestLabel } from "../lib/vocab";
  import Modal from "./Modal.svelte";
  import NeighborhoodPanel from "./NeighborhoodPanel.svelte";
  import RowList from "./RowList.svelte";
  import type { Term } from "../lib/types";

  let {
    title = "Pick a term",
    onselect,
    onclose,
  }: {
    title?: string;
    onselect: (term: Term) => void;
    onclose: () => void;
  } = $props();

  const SCOPE = "picker";
  const DEBOUNCE_MS = 200;
  const schemes = (getConfig().schemes ?? []).filter((s) => s !== "folk");

  let scheme = $state(schemes[0] ?? "");
  let q = $state("");
  let results = $state<Term[]>([]);
  let highlight = $state(0);
  let searching = $state(false);
  let error = $state("");
  let list = $state<{ move: (delta: number) => void } | null>(null);
  let inputEl = $state<HTMLInputElement | null>(null);
  let timer: ReturnType<typeof setTimeout> | undefined;

  const current = $derived(results[highlight]);

  onMount(() => {
    pushScope(SCOPE); // silences the screen's bindings while the modal is up
    return () => {
      popScope(SCOPE);
      clearTimeout(timer);
    };
  });

  function setScheme(s: string): void {
    scheme = s;
    void search(q);
    inputEl?.focus();
  }

  function onInput(): void {
    clearTimeout(timer);
    timer = setTimeout(() => void search(q), DEBOUNCE_MS);
  }

  async function search(query: string): Promise<void> {
    if (!scheme || query.trim() === "") {
      results = [];
      highlight = 0;
      return;
    }
    searching = true;
    error = "";
    try {
      const res = await searchTerms(scheme, query);
      results = res.terms ?? [];
      highlight = 0;
    } catch {
      results = [];
      error = "term search failed";
    } finally {
      searching = false;
    }
  }

  function onInputKeydown(ev: KeyboardEvent): void {
    if (ev.key === "ArrowDown") {
      ev.preventDefault();
      list?.move(1);
    } else if (ev.key === "ArrowUp") {
      ev.preventDefault();
      list?.move(-1);
    } else if (ev.key === "Enter") {
      ev.preventDefault();
      if (current) onselect(current);
    }
  }
</script>

<Modal ariaLabel={title} {onclose} width="52rem">
  <header class="head">
    <h2>{title}</h2>
    <button class="button button--quiet" onclick={onclose}>Close</button>
  </header>

  {#if schemes.length === 0}
    <p class="muted">No controlled vocabularies are loaded on this deployment.</p>
  {:else}
    <div class="tabs" role="group" aria-label="Vocabulary scheme">
      {#each schemes as s (s)}
        <button class="tab" class:active={s === scheme} aria-pressed={s === scheme} onclick={() => setScheme(s)}>
          {s}
        </button>
      {/each}
    </div>

    <label class="muted" for="vp-q">Search {scheme}</label>
    <input
      id="vp-q"
      type="search"
      data-autofocus
      bind:this={inputEl}
      bind:value={q}
      oninput={onInput}
      onkeydown={onInputKeydown}
      autocomplete="off"
      placeholder="Type to search…"
    />
    <p class="muted status" aria-live="polite">
      {#if searching}
        Searching…
      {:else if error}
        <span class="error">{error}</span>
      {:else if q.trim() && results.length === 0}
        No matches.
      {:else if results.length > 0}
        {results.length} match{results.length === 1 ? "" : "es"} -- arrows to highlight, Enter to pick
      {:else}
        Type to search.
      {/if}
    </p>

    <div class="cols">
      <div class="options">
        <RowList
          bind:this={list}
          items={results}
          bind:selected={highlight}
          getKey={(t) => t.id}
          ariaLabel="Matching terms"
        >
          {#snippet row(t: Term)}
            <button class="opt" onclick={() => onselect(t)}>
              <span class="opt-label">{bestLabel(t)}</span>
              <span class="opt-id">{t.id}</span>
            </button>
          {/snippet}
        </RowList>
      </div>

      {#if current}
        <aside class="details" aria-label="Term details">
          <h3>{bestLabel(current)}</h3>
          <p class="opt-id">{current.id}</p>
          {#if bestDefinition(current)}
            <p class="def">{bestDefinition(current)}</p>
          {/if}
          {#if allAltLabels(current).length > 0}
            <p class="alt"><span class="muted">Also known as:</span> {allAltLabels(current).join("; ")}</p>
          {/if}
          {#key current.scheme + " " + current.id}
            <NeighborhoodPanel term={current} {onselect} />
          {/key}
        </aside>
      {/if}
    </div>
  {/if}
</Modal>

<style>
  .head {
    display: flex;
    align-items: baseline;
    gap: 1rem;
  }
  .head h2 {
    margin: 0.25rem 0;
    flex: 1;
  }
  .tabs {
    display: flex;
    gap: 0.4rem;
    flex-wrap: wrap;
    margin: 0.5rem 0 0.75rem;
  }
  .tab {
    background: var(--surface);
    border: 1px solid var(--rule);
    border-radius: 999px;
    padding: 0.2em 0.9em;
    color: var(--ink-muted);
    font-size: 0.85rem;
    font-weight: 600;
  }
  .tab.active {
    background: var(--accent);
    border-color: var(--accent);
    color: var(--accent-ink);
  }
  label {
    display: block;
    font-size: 0.85rem;
    margin-bottom: 0.2rem;
  }
  #vp-q {
    width: 100%;
    font-size: 1rem;
  }
  .status {
    margin: 0.35rem 0;
    font-size: 0.85rem;
  }
  .cols {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 1rem;
    align-items: start;
  }
  @media (max-width: 40rem) {
    .cols {
      grid-template-columns: 1fr;
    }
  }
  .options {
    max-height: 22rem;
    overflow-y: auto;
  }
  .opt {
    display: block;
    width: 100%;
    text-align: left;
    background: none;
    border: 0;
    padding: 0.4rem 0.5rem;
    color: inherit;
  }
  .opt-label {
    display: block;
    font-weight: 600;
    color: var(--accent);
  }
  .opt-id {
    display: block;
    font-family: var(--mono);
    font-size: 0.72rem;
    color: var(--ink-muted);
    word-break: break-all;
    margin: 0.1rem 0;
  }
  .details {
    border: 1px solid var(--rule);
    border-radius: 6px;
    padding: 0.6rem 0.9rem;
    max-height: 22rem;
    overflow-y: auto;
  }
  .details h3 {
    margin: 0.2rem 0;
  }
  .def {
    font-size: 0.9rem;
  }
  .alt {
    font-size: 0.85rem;
  }
</style>
