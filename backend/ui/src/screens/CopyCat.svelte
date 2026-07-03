<script lang="ts">
  // Copy cataloging (tasks/050): search external Z39.50/SRU targets, stage
  // hits (or a .mrc upload) into a reviewable batch, then triage the batch
  // by keyboard in CopycatReview. Targets are admin-configured. Search
  // results, picks, and the open batch live in screenState so a drill-in
  // to a matched work returns to the same spot.
  import { onDestroy, onMount } from "svelte";
  import {
    ApiError,
    copycatSearch,
    deleteCopycatBatch,
    deleteCopycatTarget,
    fetchCopycatBatch,
    fetchCopycatBatches,
    fetchCopycatTargets,
    putCopycatTarget,
    stageCopycatBatch,
  } from "../lib/api";
  import { bindKeys, popScope, pushScope } from "../lib/keyboard";
  import { screenState } from "../lib/screenState.svelte";
  import { sessionStore } from "../lib/stores";
  import CopycatResults from "../components/CopycatResults.svelte";
  import CopycatReview from "../components/CopycatReview.svelte";
  import type { CopycatBatch, CopycatSearchResult, CopycatStagedRecord, CopycatTarget } from "../lib/types";

  const SCOPE = "copycat";

  const st = screenState("copycat", () => ({
    query: "",
    results: [] as CopycatSearchResult[],
    failures: {} as Record<string, string>,
    picked: {} as Record<number, boolean>,
    resultsSelected: 0,
    batches: [] as CopycatBatch[],
    openBatch: null as CopycatBatch | null,
    openRecords: [] as CopycatStagedRecord[],
  }));

  let targets = $state<CopycatTarget[]>([]);
  let newTarget = $state<CopycatTarget>({ name: "", url: "", protocol: "sru" });
  let busy = $state(false);
  let status = $state("");
  let error = $state("");

  const isAdmin = $derived(($sessionStore?.roles ?? []).includes("admin"));

  // The scope pushes at init (not onMount) so a review pane restored from
  // screenState -- whose child onMount runs first -- stacks on top of it.
  pushScope(SCOPE);
  onDestroy(() => popScope(SCOPE));

  onMount(() => {
    const unbind = bindKeys(SCOPE, {
      "/": { description: "focus the search box", legend: "search", handler: focusSearch },
    });
    void loadTargets();
    void loadBatches();
    return unbind;
  });

  function focusSearch(): void {
    document.getElementById("cc-q")?.focus();
  }

  async function loadTargets(): Promise<void> {
    try {
      targets = (await fetchCopycatTargets()).targets ?? [];
    } catch {
      targets = [];
    }
  }

  async function loadBatches(): Promise<void> {
    try {
      st.batches = (await fetchCopycatBatches()).batches ?? [];
    } catch {
      st.batches = [];
    }
  }

  async function addTarget(): Promise<void> {
    error = "";
    try {
      await putCopycatTarget($state.snapshot(newTarget));
      newTarget = { name: "", url: "", protocol: "sru" };
      await loadTargets();
    } catch (e) {
      error = e instanceof ApiError ? e.message : "saving the target failed";
    }
  }

  async function removeTarget(name: string): Promise<void> {
    try {
      await deleteCopycatTarget(name);
      await loadTargets();
    } catch {
      error = "deleting the target failed";
    }
  }

  async function search(): Promise<void> {
    busy = true;
    error = "";
    status = "";
    st.results = [];
    st.picked = {};
    st.resultsSelected = 0;
    try {
      const res = await copycatSearch(st.query);
      st.results = res.results ?? [];
      st.failures = res.failures ?? {};
    } catch (e) {
      error = e instanceof ApiError ? e.message : "search failed";
    } finally {
      busy = false;
    }
  }

  async function stagePicked(): Promise<void> {
    const records = st.results.filter((_, i) => st.picked[i]).map((r) => $state.snapshot(r.record));
    if (records.length === 0) return;
    busy = true;
    error = "";
    try {
      const res = await stageCopycatBatch({ label: `search: ${st.query}`, source: "search", records });
      status = `staged ${res.records.length} record${res.records.length === 1 ? "" : "s"}`;
      st.picked = {};
      await loadBatches();
      await open(res.batch.id);
    } catch (e) {
      error = e instanceof ApiError ? e.message : "staging failed";
    } finally {
      busy = false;
    }
  }

  async function upload(ev: Event): Promise<void> {
    const input = ev.currentTarget as HTMLInputElement;
    const file = input.files?.[0];
    if (!file) return;
    busy = true;
    error = "";
    try {
      const buf = new Uint8Array(await file.arrayBuffer());
      let bin = "";
      for (const b of buf) bin += String.fromCharCode(b);
      const res = await stageCopycatBatch({ label: file.name, mrc: btoa(bin) });
      status = `staged ${res.records.length} record${res.records.length === 1 ? "" : "s"} from ${file.name}`;
      await loadBatches();
      await open(res.batch.id);
    } catch (e) {
      error = e instanceof ApiError ? e.message : "upload failed";
    } finally {
      busy = false;
      input.value = "";
    }
  }

  async function open(id: string): Promise<void> {
    error = "";
    try {
      const res = await fetchCopycatBatch(id);
      st.openBatch = res.batch;
      st.openRecords = res.records ?? [];
    } catch {
      error = "loading the batch failed";
    }
  }

  function closeBatch(): void {
    st.openBatch = null;
    st.openRecords = [];
  }

  function committed(done: CopycatBatch): void {
    status = `committed ${done.committed} record${done.committed === 1 ? "" : "s"}, ${done.skipped} skipped (${done.policy})`;
    void loadBatches();
  }

  async function removeBatch(id: string): Promise<void> {
    try {
      await deleteCopycatBatch(id);
      if (st.openBatch?.id === id) closeBatch();
      await loadBatches();
    } catch {
      error = "deleting the batch failed";
    }
  }
</script>

<main class="wide">
  <h1>Copy cataloging</h1>

  <details class="targets">
    <summary>Search targets ({targets.length})</summary>
    <ul class="tlist">
      {#each targets as t (t.name)}
        <li>
          <span class="mono">{t.name}</span> · {t.protocol} · <span class="muted">{t.url}</span>
          {#if isAdmin}
            <button class="button button--quiet mini" onclick={() => void removeTarget(t.name)}>Remove</button>
          {/if}
        </li>
      {:else}
        <li class="muted">No targets configured{isAdmin ? "" : " -- ask an admin"}.</li>
      {/each}
    </ul>
    {#if isAdmin}
      <div class="row">
        <input aria-label="Target name" bind:value={newTarget.name} placeholder="name (e.g. loc)" />
        <input class="grow" aria-label="Target URL" bind:value={newTarget.url} placeholder="SRU base URL or z3950 host:port/DB" />
        <select aria-label="Protocol" bind:value={newTarget.protocol}>
          <option value="sru">SRU</option>
          <option value="z3950">Z39.50</option>
        </select>
        <button class="button" onclick={() => void addTarget()}>Add target</button>
      </div>
    {/if}
  </details>

  <section aria-label="External search">
    <h2>Search external targets</h2>
    <div class="row">
      <input
        id="cc-q"
        class="grow"
        aria-label="Search query"
        bind:value={st.query}
        placeholder="title, author, ISBN…"
        onkeydown={(ev) => ev.key === "Enter" && void search()}
      />
      <button class="button" onclick={() => void search()} disabled={busy || !st.query.trim()}>Search</button>
      <label class="button button--quiet upload-btn">
        Stage a .mrc file… <input type="file" accept=".mrc,.marc" onchange={(ev) => void upload(ev)} hidden />
      </label>
    </div>
    <p aria-live="polite">
      {#if busy}<span class="muted">Working…</span>{/if}
      {#if status}<span class="ok">{status}</span>{/if}
      {#if error}<span class="error">{error}</span>{/if}
      {#each Object.entries(st.failures) as [name, msg] (name)}
        <span class="error">{name}: {msg}</span>
      {/each}
    </p>

    {#if st.results.length > 0 && !st.openBatch}
      <CopycatResults
        results={st.results}
        bind:picked={st.picked}
        bind:selected={st.resultsSelected}
        {busy}
        onstage={() => void stagePicked()}
      />
    {/if}
  </section>

  <section aria-label="Staged batches" class="split">
    <div>
      <h2>Staged batches</h2>
      <ul class="blist">
        {#each st.batches as b (b.id)}
          <li class:open={st.openBatch?.id === b.id}>
            <button class="blabel" onclick={() => void open(b.id)}>
              {b.label} <span class="muted">· {b.records} records · {b.source}</span>
              <span class="badge" data-status={b.status}>{b.status}</span>
            </button>
            <button class="button button--quiet mini" onclick={() => void removeBatch(b.id)}>Delete</button>
          </li>
        {:else}
          <li class="muted">Nothing staged yet. Search a target or stage a .mrc file to review records here.</li>
        {/each}
      </ul>
    </div>

    <div>
      {#if st.openBatch}
        <CopycatReview
          bind:batch={st.openBatch}
          bind:records={st.openRecords}
          onclose={closeBatch}
          oncommitted={committed}
        />
      {/if}
    </div>
  </section>
</main>

<style>
  h2 {
    font-size: 1rem;
    margin: 1.2rem 0 0.5rem;
  }
  .targets {
    margin: 0.6rem 0;
  }
  .targets summary {
    cursor: pointer;
    color: var(--ink-muted);
  }
  .tlist {
    list-style: none;
    padding: 0;
    margin: 0.4rem 0;
  }
  .tlist li {
    padding: 0.2rem 0;
  }
  .row {
    display: flex;
    gap: 0.5rem;
    align-items: center;
    flex-wrap: wrap;
    margin: 0.4rem 0;
  }
  .grow {
    flex: 1;
    min-width: 14rem;
    max-width: 30rem;
  }
  .mono {
    font-family: var(--mono);
    font-size: 0.85em;
  }
  .mini {
    font-size: 0.72rem;
    padding: 0.05em 0.6em;
  }
  .upload-btn {
    cursor: pointer;
  }
  .blist {
    list-style: none;
    padding: 0;
    margin: 0.3rem 0;
  }
  .blist li {
    display: flex;
    gap: 0.6rem;
    align-items: center;
    border-bottom: 1px solid var(--rule);
    padding: 0.25rem 0;
  }
  .blist li.open .blabel {
    color: var(--accent);
  }
  .blabel {
    background: none;
    border: 0;
    padding: 0.2rem 0;
    color: inherit;
    text-align: left;
    flex: 1;
    font-weight: 600;
  }
  .badge {
    font-size: 0.7rem;
    font-weight: 700;
    border-radius: 999px;
    padding: 0.08em 0.6em;
    border: 1px solid var(--rule);
    margin-left: 0.5em;
  }
  .badge[data-status="COMMITTED"] {
    background: var(--accent);
    border-color: var(--accent);
    color: var(--accent-ink);
  }
  .ok {
    color: var(--accent);
  }
</style>
