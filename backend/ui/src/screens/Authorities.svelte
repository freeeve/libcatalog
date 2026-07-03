<script lang="ts">
  // Local-authority management: debounced label search over /v1/authorities
  // with keyboard-navigable results, and the one-keystroke "create local
  // authority" flow -- an unmatched heading becomes a term with n or the
  // Create button, landing directly in its editor (tasks/046).
  import { onMount } from "svelte";
  import { fetchAuthorities, createAuthority, ApiError } from "../lib/api";
  import { bindKeys, pushScope, popScope } from "../lib/keyboard";
  import { navigate } from "../lib/router";
  import { bestLabel } from "../lib/vocab";
  import RowList from "../components/RowList.svelte";
  import type { Term } from "../lib/types";

  const SCOPE = "authorities";
  const DEBOUNCE_MS = 250;

  let q = $state("");
  let terms = $state<Term[]>([]);
  let selected = $state(0);
  let error = $state("");
  let loading = $state(false);
  let creating = $state(false);
  let timer: ReturnType<typeof setTimeout> | undefined;

  // The create affordance shows for a non-empty query with no exact label hit.
  const canCreate = $derived(
    q.trim() !== "" && !terms.some((t) => bestLabel(t).trim().toLowerCase() === q.trim().toLowerCase()),
  );

  onMount(() => {
    pushScope(SCOPE);
    const unbind = bindKeys(SCOPE, {
      n: { description: "create the typed heading", legend: "new heading", handler: () => void create() },
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
      const page = await fetchAuthorities(query);
      terms = page.terms ?? [];
      selected = 0;
    } catch (e) {
      terms = [];
      error = e instanceof ApiError && e.status === 401 ? "session expired -- sign in again" : "search failed";
    } finally {
      loading = false;
    }
  }

  async function create(): Promise<void> {
    const label = q.trim();
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

<main>
  <h1>Authorities</h1>
  <p>
    <label for="auth-q" class="muted">Local headings (preferred or used-for label)</label>
  </p>
  <input id="auth-q" type="search" bind:value={q} oninput={onInput} placeholder="Search local authorities…" autocomplete="off" />
  <p class="muted" aria-live="polite">
    {#if loading}
      Searching…
    {:else if error}
      <span class="error">{error}</span>
    {:else}
      {terms.length} term{terms.length === 1 ? "" : "s"}
    {/if}
  </p>

  {#if canCreate}
    <p>
      <button class="button" onclick={() => void create()} disabled={creating}>
        {creating ? "Creating…" : `Create local authority "${q.trim()}"`}
      </button>
      <span class="muted kbd-hint">(n)</span>
    </p>
  {/if}

  <RowList items={terms} bind:selected getKey={(t) => t.id} ariaLabel="Local authority terms" scope={SCOPE} itemName="term" onactivate={open}>
    {#snippet row(t: Term)}
      <a class="row-link" href={"#/authorities/" + encodeURIComponent(localId(t))}>
        <span class="label">
          {bestLabel(t)}
          {#if t.mergedInto}<span class="retired">merged</span>{/if}
        </span>
        {#if t.altLabels && Object.values(t.altLabels).flat().length > 0}
          <span class="muted">UF: {Object.values(t.altLabels).flat().join("; ")}</span>
        {/if}
        <span class="id">{t.id}</span>
      </a>
    {/snippet}
  </RowList>
</main>

<style>
  #auth-q {
    width: 100%;
    max-width: 28rem;
    font-size: 1rem;
  }
  .kbd-hint {
    font-size: 0.85rem;
  }
  .row-link {
    display: grid;
    grid-template-columns: 1fr auto;
    gap: 0.1rem 0.9rem;
    padding: 0.5rem 0.6rem;
    text-decoration: none;
    color: inherit;
  }
  .label {
    font-weight: 600;
    color: var(--accent);
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
    font-size: 0.78rem;
    color: var(--ink-muted);
    grid-column: 2;
    grid-row: 1;
  }
</style>
