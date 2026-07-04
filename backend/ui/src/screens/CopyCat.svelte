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
    deleteCopycatProfile,
    deleteCopycatTarget,
    fetchCopycatBatch,
    fetchCopycatBatches,
    fetchCopycatProfiles,
    fetchCopycatTargets,
    putCopycatProfile,
    putCopycatTarget,
    stageCopycatBatch,
  } from "../lib/api";
  import { bindKeys, popScope, pushScope } from "../lib/keyboard";
  import { screenState } from "../lib/screenState.svelte";
  import { sessionStore } from "../lib/stores";
  import CopycatResults from "../components/CopycatResults.svelte";
  import CopycatReview from "../components/CopycatReview.svelte";
  import type {
    CopycatBatch,
    CopycatFieldTerm,
    CopycatPolicy,
    CopycatProfile,
    CopycatSearchResult,
    CopycatStagedRecord,
    CopycatTarget,
  } from "../lib/types";

  /** Well-known open targets an admin can add in one click: the big free
   *  copy-cataloging sources speak Z39.50/SRU anonymously (subscription
   *  targets like OCLC need credentials, which targets don't carry yet). */
  const SUGGESTED_TARGETS: (CopycatTarget & { blurb: string })[] = [
    { name: "loc", url: "lx2.loc.gov:210/LCDB", protocol: "z3950", blurb: "Library of Congress (Z39.50, anonymous)" },
    { name: "loc-sru", url: "http://lx2.loc.gov:210/LCDB", protocol: "sru", blurb: "Library of Congress (SRU)" },
    { name: "k10plus", url: "https://sru.k10plus.de/opac-de-627", protocol: "sru", blurb: "K10plus German union catalogue (SRU)" },
    { name: "indexdata-test", url: "z3950.indexdata.com:210/marc", protocol: "z3950", blurb: "Index Data public test server (tiny sample set)" },
  ];

  /** The fielded access points shared by both protocols (tasks/074). */
  const FIELD_INDEXES = [
    { index: "title", label: "Title" },
    { index: "author", label: "Author" },
    { index: "subject", label: "Subject" },
    { index: "isbn", label: "ISBN" },
    { index: "issn", label: "ISSN" },
    { index: "lccn", label: "LCCN" },
    { index: "id", label: "Record id" },
  ] as const;

  const SCOPE = "copycat";

  /** A ?batch= deep link (the tasks/077 stage flow) opens that batch. */
  let { batchId = "" }: { batchId?: string } = $props();

  const st = screenState("copycat", () => ({
    query: "",
    advanced: false,
    fields: { title: "", author: "", subject: "", isbn: "", issn: "", lccn: "", id: "" },
    results: [] as CopycatSearchResult[],
    failures: {} as Record<string, string>,
    picked: {} as Record<number, boolean>,
    resultsSelected: 0,
    batches: [] as CopycatBatch[],
    openBatch: null as CopycatBatch | null,
    openRecords: [] as CopycatStagedRecord[],
    profileName: "",
  }));

  let targets = $state<CopycatTarget[]>([]);
  let newTarget = $state<CopycatTarget>({ name: "", url: "", protocol: "sru" });
  let profiles = $state<CopycatProfile[]>([]);
  let newProfile = $state<{ name: string; policy: CopycatPolicy; targets: Record<string, boolean> }>({
    name: "",
    policy: "replace-feed",
    targets: {},
  });
  let busy = $state(false);
  let status = $state("");
  let error = $state("");

  /** The active staging profile: its targets scope the search, its policy
   *  pre-sets staged batches (tasks/068). */
  const profile = $derived(profiles.find((p) => p.name === st.profileName) ?? null);

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
    void loadProfiles();
    if (batchId) void open(batchId);
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

  async function loadProfiles(): Promise<void> {
    try {
      profiles = (await fetchCopycatProfiles()).profiles ?? [];
    } catch {
      profiles = [];
    }
  }

  async function saveProfile(): Promise<void> {
    error = "";
    const picked = Object.entries(newProfile.targets)
      .filter(([, on]) => on)
      .map(([name]) => name);
    try {
      await putCopycatProfile({ name: newProfile.name.trim(), targets: picked, policy: newProfile.policy });
      st.profileName = newProfile.name.trim();
      newProfile = { name: "", policy: "replace-feed", targets: {} };
      await loadProfiles();
    } catch (e) {
      error = e instanceof ApiError ? e.message : "saving the profile failed";
    }
  }

  async function removeProfile(name: string): Promise<void> {
    try {
      await deleteCopycatProfile(name);
      if (st.profileName === name) st.profileName = "";
      await loadProfiles();
    } catch {
      error = "deleting the profile failed";
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

  /** Suggested targets not yet configured (matched by name). */
  const suggestions = $derived(SUGGESTED_TARGETS.filter((s) => !targets.some((t) => t.name === s.name)));

  async function addSuggested(s: CopycatTarget): Promise<void> {
    error = "";
    try {
      await putCopycatTarget({ name: s.name, url: s.url, protocol: s.protocol });
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

  /** Non-empty fielded terms; entries outside the collapsed advanced form
   *  are ignored so a leftover value can't silently narrow a quick search. */
  const fieldTerms = $derived(
    st.advanced
      ? FIELD_INDEXES.filter(({ index }) => st.fields[index].trim() !== "").map(
          ({ index }): CopycatFieldTerm => ({ index, term: st.fields[index].trim() }),
        )
      : [],
  );

  const canSearch = $derived(st.query.trim() !== "" || fieldTerms.length > 0);

  async function search(): Promise<void> {
    if (!canSearch) return;
    busy = true;
    error = "";
    status = "";
    st.results = [];
    st.picked = {};
    st.resultsSelected = 0;
    try {
      const res = await copycatSearch(st.query.trim(), fieldTerms, profile?.targets ?? undefined);
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
      const res = await stageCopycatBatch({
        label: `search: ${[st.query.trim(), ...fieldTerms.map((f) => `${f.index}=${f.term}`)].filter(Boolean).join(" ")}`,
        source: "search",
        records,
        ...(profile?.policy ? { policy: profile.policy } : {}),
      });
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
      const res = await stageCopycatBatch({
        label: file.name,
        mrc: btoa(bin),
        ...(profile?.policy ? { policy: profile.policy } : {}),
      });
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
      {#if suggestions.length > 0}
        <div class="suggested">
          <span class="muted">Suggested (open, no credentials needed):</span>
          {#each suggestions as s (s.name)}
            <button class="button button--quiet mini" title={s.url + " (" + s.protocol + ")"} onclick={() => void addSuggested(s)}>
              + {s.blurb}
            </button>
          {/each}
        </div>
      {/if}
    {/if}
  </details>

  <details class="targets">
    <summary>Staging profiles ({profiles.length})</summary>
    <ul class="tlist">
      {#each profiles as p (p.name)}
        <li>
          <span class="mono">{p.name}</span> ·
          <span class="muted">{p.targets?.length ? p.targets.join(", ") : "all targets"} · {p.policy || "replace-feed"}</span>
          <button class="button button--quiet mini" onclick={() => void removeProfile(p.name)}>Remove</button>
        </li>
      {:else}
        <li class="muted">No profiles saved. A profile remembers target choices and overlay policy for recurring imports.</li>
      {/each}
    </ul>
    <div class="row">
      <input aria-label="Profile name" bind:value={newProfile.name} placeholder="name (e.g. weekly-loc)" />
      <select aria-label="Overlay policy" bind:value={newProfile.policy}>
        <option value="replace-feed">replace feed</option>
        <option value="fill-holes-only">fill holes only</option>
        <option value="never">never overlay</option>
      </select>
      {#each targets as t (t.name)}
        <label class="pick"><input type="checkbox" bind:checked={newProfile.targets[t.name]} /> {t.name}</label>
      {/each}
      <button class="button" onclick={() => void saveProfile()} disabled={!newProfile.name.trim()}>Save profile</button>
    </div>
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
      <select aria-label="Staging profile" bind:value={st.profileName}>
        <option value="">no profile</option>
        {#each profiles as p (p.name)}
          <option value={p.name}>{p.name}</option>
        {/each}
      </select>
      <button class="button" onclick={() => void search()} disabled={busy || !canSearch}>Search</button>
      <button class="button button--quiet" aria-expanded={st.advanced} onclick={() => (st.advanced = !st.advanced)}>
        Advanced {st.advanced ? "▴" : "▾"}
      </button>
      <label class="button button--quiet upload-btn">
        Stage a .mrc file… <input type="file" accept=".mrc,.marc" onchange={(ev) => void upload(ev)} hidden />
      </label>
      <a class="button button--quiet" href="#/copycat/new">New record…</a>
    </div>
    {#if st.advanced}
      <div class="fielded" role="group" aria-label="Fielded search">
        {#each FIELD_INDEXES as f (f.index)}
          <label class="ffield">
            <span class="muted">{f.label}</span>
            <input
              bind:value={st.fields[f.index]}
              onkeydown={(ev) => ev.key === "Enter" && void search()}
            />
          </label>
        {/each}
        <p class="muted hint">Filled fields AND together (and onto the keyword box); empty fields are omitted.</p>
      </div>
    {/if}
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
  .suggested {
    display: flex;
    gap: 0.4rem;
    align-items: center;
    flex-wrap: wrap;
    margin: 0.3rem 0 0.1rem;
    font-size: 0.82rem;
  }
  .fielded {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(13rem, 1fr));
    gap: 0.4rem 0.8rem;
    margin: 0.4rem 0 0.6rem;
    padding: 0.5rem 0.7rem;
    border: 1px solid var(--rule);
    border-radius: 8px;
  }
  .ffield {
    display: flex;
    align-items: center;
    gap: 0.45rem;
    font-size: 0.82rem;
  }
  .ffield span {
    min-width: 4.2rem;
    text-align: right;
  }
  .ffield input {
    flex: 1;
    min-width: 0;
  }
  .fielded .hint {
    grid-column: 1 / -1;
    margin: 0.1rem 0 0;
    font-size: 0.75rem;
  }
  .pick {
    font-size: 0.85rem;
    color: var(--ink-muted);
    white-space: nowrap;
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
