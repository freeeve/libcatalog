<script lang="ts">
  // Local-authority management: debounced label search over /v1/authorities
  // with keyboard-navigable results, and the one-keystroke "create local
  // authority" flow -- an unmatched heading becomes a term with n or the
  // Create button, landing directly in its editor.
  import { onMount } from "svelte";
  import { fetchAuthorities, createAuthority, ApiError } from "../lib/api";
  import { bindKeys, pushScope, popScope } from "../lib/keyboard";
  import { navigate } from "../lib/router";
  import { screenState } from "../lib/screenState.svelte";
  import { bestLabel } from "../lib/vocab";
  import RowList from "../components/RowList.svelte";
  import type { Term } from "../lib/types";

  const SCOPE = "authorities";
  const DEBOUNCE_MS = 250;
  const FRESH_MS = 60_000;

  const MAX_LIMIT = 200; // the endpoint's hard ceiling
  const PAGE = 50;

  const st = screenState("authorities", () => ({
    q: "",
    terms: [] as Term[],
    selected: 0,
    loadedAt: 0,
    // limit is how many rows this browse has asked for; total is the real count
    // of local headings, so the screen never presents the page size as a total
    // and can offer "Load more" while the page is capped.
    limit: PAGE,
    total: 0,
  }));

  // The browse is showing fewer headings than exist.
  const truncated = $derived(st.terms.length < st.total);

  let error = $state("");
  let loading = $state(false);
  let creating = $state(false);
  let timer: ReturnType<typeof setTimeout> | undefined;

  // The create affordance shows for a non-empty query with no exact label hit.
  const canCreate = $derived(
    st.q.trim() !== "" && !st.terms.some((t) => bestLabel(t).trim().toLowerCase() === st.q.trim().toLowerCase()),
  );

  onMount(() => {
    pushScope(SCOPE);
    const unbind = bindKeys(SCOPE, {
      n: { description: "create the typed heading", legend: "new heading", handler: () => void create() },
      "/": { description: "focus the search box", legend: "search", handler: focusSearch },
    });
    if (Date.now() - st.loadedAt > FRESH_MS) void search(st.q, true);
    return () => {
      unbind();
      popScope(SCOPE);
      clearTimeout(timer);
    };
  });

  function onInput(): void {
    clearTimeout(timer);
    st.limit = PAGE; // a new query starts from the first page
    timer = setTimeout(() => void search(st.q, false), DEBOUNCE_MS);
  }

  /** Runs the search; a refresh keeps the selection on the same term id. */
  async function search(query: string, refresh: boolean): Promise<void> {
    loading = true;
    error = "";
    const keepId = refresh ? st.terms[st.selected]?.id : undefined;
    try {
      const page = await fetchAuthorities(query, st.limit);
      st.terms = page.terms ?? [];
      st.total = page.total ?? st.terms.length;
      st.loadedAt = Date.now();
      const found = keepId ? st.terms.findIndex((t) => t.id === keepId) : -1;
      st.selected = found >= 0 ? found : Math.min(st.selected, Math.max(0, st.terms.length - 1));
      if (!refresh) st.selected = 0;
    } catch (e) {
      st.terms = [];
      st.total = 0;
      error = e instanceof ApiError && e.status === 401 ? "session expired -- sign in again" : "search failed";
    } finally {
      loading = false;
    }
  }

  /** Widens the browse by a page (up to the endpoint ceiling), keeping the
      selection. Past the ceiling the search box is the way to reach the rest. */
  async function loadMore(): Promise<void> {
    if (st.limit >= MAX_LIMIT) return;
    st.limit = Math.min(st.limit + PAGE, MAX_LIMIT);
    await search(st.q, true);
  }

  async function create(): Promise<void> {
    const label = st.q.trim();
    if (!label || creating || !canCreate) return;
    creating = true;
    error = "";
    try {
      const made = await createAuthority({ prefLabel: { en: label } });
      navigate(`/authorities/${encodeURIComponent(made.id)}`);
    } catch {
      error = "create failed";
    } finally {
      creating = false;
    }
  }

  function open(t: Term): void {
    navigate(`/authorities/${encodeURIComponent(localId(t))}`);
  }

  /** The minted id is the URI's trailing segment. */
  function localId(t: Term): string {
    return t.id.slice(t.id.lastIndexOf("/") + 1);
  }

  function focusSearch(): void {
    document.getElementById("auth-q")?.focus();
  }
</script>

<main class="wide" id="main" tabindex="-1">
  <h1>Authorities</h1>
  <p class="lede">
    <label for="auth-q" class="muted">Local headings (preferred or used-for label)</label>
  </p>
  <input id="auth-q" type="search" bind:value={st.q} oninput={onInput} placeholder="Search local authorities…" autocomplete="off" />
  <p class="muted status" aria-live="polite">
    {#if loading && st.terms.length === 0}
      Searching…
    {:else if error}
      <span class="error">{error}</span>
    {:else if truncated}
      Showing {st.terms.length.toLocaleString()} of {st.total.toLocaleString()} terms
    {:else}
      {st.total.toLocaleString()} term{st.total === 1 ? "" : "s"}
    {/if}
  </p>

  {#if canCreate}
    <p>
      <button class="button" onclick={() => void create()} disabled={creating}>
        {creating ? "Creating…" : `Create local authority "${st.q.trim()}"`}
      </button>
      <span class="muted kbd-hint">(n)</span>
    </p>
  {/if}

  <RowList items={st.terms} bind:selected={st.selected} getKey={(t) => t.id} ariaLabel="Local authority terms" scope={SCOPE} itemName="term" onactivate={open}>
    {#snippet row(t: Term)}
      <a class="row-link" href={"#/authorities/" + encodeURIComponent(localId(t))}>
        <span class="label">
          {bestLabel(t)}
          {#if t.mergedInto}<span class="retired">merged</span>{/if}
        </span>
        <span class="muted uf">
          {#if t.altLabels && Object.values(t.altLabels).flat().length > 0}
            UF: {Object.values(t.altLabels).flat().join("; ")}
          {/if}
        </span>
        <span class="id">{t.id}</span>
      </a>
    {/snippet}
  </RowList>

  {#if truncated}
    <p class="more">
      {#if st.limit < MAX_LIMIT}
        <button class="button button--quiet" onclick={() => void loadMore()} disabled={loading}>
          {loading ? "Loading…" : "Load more"}
        </button>
      {:else}
        <span class="muted">Showing the first {MAX_LIMIT.toLocaleString()} -- search to reach the rest.</span>
      {/if}
    </p>
  {/if}
</main>

<style>
  #auth-q {
    width: 100%;
    max-width: 28rem;
    font-size: 1rem;
  }
  .more {
    margin-top: 0.6rem;
  }
  .kbd-hint {
    font-size: 0.85rem;
  }
  .lede {
    margin: 0.2rem 0;
  }
  .status {
    margin: 0.35rem 0;
    font-size: var(--fs-meta);
  }
  .row-link {
    display: grid;
    grid-template-columns: minmax(12rem, auto) 1fr auto;
    gap: 0 0.9rem;
    align-items: baseline;
    padding: 0.22rem 0.55rem;
    text-decoration: none;
    color: inherit;
  }
  .label {
    font-weight: 600;
    color: var(--accent);
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .uf {
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .retired {
    font-size: 0.72rem;
    font-weight: 700;
    text-transform: uppercase;
    color: var(--ink-muted);
    border: 1px solid var(--rule);
    border-radius: 999px;
    padding: 0.05em 0.6em;
    margin-left: 0.5em;
  }
  .id {
    font-family: var(--mono);
    font-size: var(--fs-meta);
    color: var(--ink-muted);
  }
</style>
