<script lang="ts">
  // Modal vocabulary picker: scheme tabs over config.schemes (folk excluded)
  // plus one live tab per registered suggest source (tasks/067), search-as-
  // you-type against /v1/terms (local index) or /v1/vocabsuggest (live
  // proxy), arrow-key result navigation, and a details pane for the
  // highlighted term. Modal owns the trap/Escape/focus restore; Enter or
  // click emits the term through onselect.
  import { onMount } from "svelte";
  import { cacheVocabTerm, fetchVocabSources, searchTerms, vocabSuggest } from "../lib/api";
  import { getConfig } from "../lib/config";
  import { popScope, pushScope } from "../lib/keyboard";
  import { allAltLabels, bestDefinition, bestLabel } from "../lib/vocab";
  import Modal from "./Modal.svelte";
  import NeighborhoodPanel from "./NeighborhoodPanel.svelte";
  import RowList from "./RowList.svelte";
  import type { Term, VocabSuggestion } from "../lib/types";

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

  /** One tab: a locally indexed scheme, or a live suggest source. */
  interface Tab {
    key: string;
    label: string;
    live: boolean;
    scheme: string; // index scheme, or the live source's scheme
    source?: string; // live source name (live tabs only)
  }

  let tabs = $state<Tab[]>(schemes.map((s) => ({ key: s, label: s, live: false, scheme: s })));
  let active = $state(0);
  let q = $state("");
  let results = $state<Term[]>([]);
  // The last live search's raw suggestions by URI, so a pick can cache its
  // label into the local index (tasks/072).
  let liveSuggs: Record<string, VocabSuggestion> = {};
  let highlight = $state(0);
  let searching = $state(false);
  let error = $state("");
  let list = $state<{ move: (delta: number) => void } | null>(null);
  let inputEl = $state<HTMLInputElement | null>(null);
  let timer: ReturnType<typeof setTimeout> | undefined;

  const tab = $derived(tabs[active]);
  const current = $derived(results[highlight]);

  onMount(() => {
    pushScope(SCOPE); // silences the screen's bindings while the modal is up
    // Live tabs load lazily; a deployment without the source registry (or a
    // fetch hiccup) just keeps the local tabs.
    fetchVocabSources().then(
      (r) =>
        (tabs = [
          ...tabs,
          ...(r.sources ?? [])
            .filter((s) => !!s.suggestUrl && !!s.suggestFlavor)
            .map((s) => ({ key: "live:" + s.name, label: s.name, live: true, scheme: s.scheme, source: s.name })),
        ]),
      () => {},
    );
    return () => {
      popScope(SCOPE);
      clearTimeout(timer);
    };
  });

  function setTab(i: number): void {
    active = i;
    void search(q);
    inputEl?.focus();
  }

  function onInput(): void {
    clearTimeout(timer);
    timer = setTimeout(() => void search(q), DEBOUNCE_MS);
  }

  async function search(query: string): Promise<void> {
    if (!tab || query.trim() === "") {
      results = [];
      highlight = 0;
      return;
    }
    searching = true;
    error = "";
    try {
      if (tab.live && tab.source) {
        const res = await vocabSuggest(tab.source, query);
        liveSuggs = Object.fromEntries((res.suggestions ?? []).map((s) => [s.id, s]));
        results = (res.suggestions ?? []).map((s) => ({
          scheme: s.scheme,
          id: s.id,
          labels: { en: s.label },
          ...(s.description ? { definition: { en: s.description } } : {}),
          ...(s.variants?.length ? { altLabels: { en: s.variants } } : {}),
          ...(s.exactMatch ? { exactMatch: s.exactMatch } : {}),
        }));
      } else {
        const res = await searchTerms(tab.scheme, query);
        results = res.terms ?? [];
      }
      highlight = 0;
    } catch {
      results = [];
      error = tab.live ? "live source unavailable" : "term search failed";
    } finally {
      searching = false;
    }
  }

  /** Emits the pick; a live-source pick first caches its label into the
   *  local index (fire-and-forget) so the stored URI resolves forever. */
  function pick(t: Term): void {
    const sugg = liveSuggs[t.id];
    if (tab?.live && sugg) {
      cacheVocabTerm(sugg).catch(() => {});
    }
    onselect(t);
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
      if (current) pick(current);
    }
  }
</script>

<Modal ariaLabel={title} {onclose} width="52rem">
  <header class="head">
    <h2>{title}</h2>
    <button class="button button--quiet" onclick={onclose}>Close</button>
  </header>

  {#if tabs.length === 0}
    <p class="muted">No controlled vocabularies are loaded on this deployment.</p>
  {:else}
    <div class="tabs" role="group" aria-label="Vocabulary scheme">
      {#each tabs as t, i (t.key)}
        <button class="tab" class:active={i === active} aria-pressed={i === active} onclick={() => setTab(i)}>
          {t.label}{#if t.live}<span class="live">live</span>{/if}
        </button>
      {/each}
    </div>

    <label class="muted" for="vp-q">Search {tab?.label ?? ""}</label>
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
            <button class="opt" onclick={() => pick(t)}>
              <span class="opt-label">{bestLabel(t)}</span>
              {#if t.path?.length}
                <span class="opt-path">{t.path.map((p) => p.label).join(" › ")}</span>
              {:else}
                <span class="opt-id">{t.id}</span>
              {/if}
            </button>
          {/snippet}
        </RowList>
      </div>

      {#if current}
        <aside class="details" aria-label="Term details">
          <h3>
            {#if current.path?.length}
              <span class="path">{current.path.map((p) => p.label).join(" › ") + " › "}</span>
            {/if}{bestLabel(current)}
          </h3>
          <p class="opt-id">{current.id}</p>
          {#if bestDefinition(current)}
            <p class="def">{bestDefinition(current)}</p>
          {/if}
          {#if allAltLabels(current).length > 0}
            <p class="alt"><span class="muted">Also known as:</span> {allAltLabels(current).join("; ")}</p>
          {/if}
          {#if tab?.live}
            {#if current.exactMatch?.length}
              <p class="alt"><span class="muted">Same as:</span></p>
              <ul class="xm">
                {#each current.exactMatch as uri (uri)}
                  <li class="opt-id">{uri}</li>
                {/each}
              </ul>
            {/if}
          {:else}
            {#key current.scheme + " " + current.id}
              <NeighborhoodPanel term={current} {onselect} />
            {/key}
          {/if}
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
  .live {
    margin-left: 0.4em;
    font-size: 0.68rem;
    text-transform: uppercase;
    letter-spacing: 0.04em;
    opacity: 0.75;
  }
  .xm {
    margin: 0.2rem 0 0;
    padding-left: 1rem;
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
  .opt-path {
    display: block;
    font-size: 0.78rem;
    color: var(--ink-muted);
    margin: 0.1rem 0;
  }
  .path {
    font-weight: 400;
    color: var(--ink-muted);
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
