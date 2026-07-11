<script lang="ts">
  // Copy cataloging: search external Z39.50/SRU targets, stage
  // hits (or a .mrc upload) into a reviewable batch, then triage the batch
  // by keyboard in CopycatReview. Targets are admin-configured. Search
  // results, picks, and the open batch live in screenState so a drill-in
  // to a matched work returns to the same spot.
  import { onDestroy, onMount } from "svelte";
  import {
    humanApiMessage,
    copycatSearch,
    deleteCopycatBatch,
    deleteCopycatProfile,
    deleteCopycatTarget,
    fetchCopycatBatch,
    fetchCopycatBatches,
    fetchCopycatProfiles,
    fetchCopycatTargets,
    fetchSuggestedTargets,
    putCopycatProfile,
    putCopycatTarget,
    stageCopycatBatch,
  } from "../lib/api";
  import { isReadOnly } from "../lib/config";
  import { bindKeys, popScope, pushScope } from "../lib/keyboard";
  import { normalizeIsbn } from "../lib/isbn";
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

  /** Blurbs for the one-click presets, keyed by target name. The preset *config*
   *  (url/protocol/indexes/version/schema) is served from the backend so it
   *  cannot drift from the seeded defaults -- how the k10plus preset came to lack
   * its PICA indexes. These are the human copy the wire config lacks;
   *  a preset with no blurb falls back to its name. */
  const SUGGESTED_BLURBS: Record<string, string> = {
    loc: "Library of Congress (Z39.50, anonymous)",
    "loc-sru": "Library of Congress (SRU)",
    k10plus: "K10plus German union catalogue (SRU)",
    "indexdata-test": "Index Data public test server (tiny sample set)",
  };

  /** The fielded access points shared by both protocols. */
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

  /** A ?batch= deep link (the stage flow) opens that batch. */
  let { batchId = "" }: { batchId?: string } = $props();

  const st = screenState("copycat", () => ({
    query: "",
    advanced: false,
    fields: { title: "", author: "", subject: "", isbn: "", issn: "", lccn: "", id: "" },
    results: [] as CopycatSearchResult[],
    failures: {} as Record<string, string>,
    warnings: {} as Record<string, string>,
    picked: {} as Record<number, boolean>,
    resultsSelected: 0,
    batches: [] as CopycatBatch[],
    openBatch: null as CopycatBatch | null,
    openRecords: [] as CopycatStagedRecord[],
    profileName: "",
  }));

  let targets = $state<CopycatTarget[]>([]);
  let suggestedTargets = $state<CopycatTarget[]>([]);
  let newTarget = $state<CopycatTarget>({ name: "", url: "", protocol: "sru" });
  // The add form's advanced SRU knobs: version/schema bind onto
  // newTarget; index overrides are a repeatable access-point -> CQL-index list
  // assembled into newTarget.indexes on submit.
  let newIndexes = $state<{ index: string; cql: string }[]>([]);
  let profiles = $state<CopycatProfile[]>([]);
  let newProfile = $state<{ name: string; policy: CopycatPolicy; targets: Record<string, boolean> }>({
    name: "",
    policy: "replace-feed",
    targets: {},
  });
  let busy = $state(false);
  let status = $state("");
  let error = $state("");

  /** Quick-add: scan or paste one ISBN, search the isbn index, and either drop
   *  the hits into the results list (default) or stage the best hit straight
   *  into a batch (auto-stage). */
  let quickIsbn = $state("");
  let quickAutoStage = $state(false);

  /** The active staging profile: its targets scope the search, its policy
   * pre-sets staged batches. */
  const profile = $derived(profiles.find((p) => p.name === st.profileName) ?? null);

  const isAdmin = $derived(($sessionStore?.roles ?? []).includes("admin"));
  const readOnly = isReadOnly();

  // The scope pushes at init (not onMount) so a review pane restored from
  // screenState -- whose child onMount runs first -- stacks on top of it.
  pushScope(SCOPE);
  onDestroy(() => popScope(SCOPE));

  onMount(() => {
    const unbind = bindKeys(SCOPE, {
      "/": { description: "focus the search box", legend: "search", handler: focusSearch },
    });
    void loadTargets();
    void loadSuggested();
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

  async function loadSuggested(): Promise<void> {
    try {
      suggestedTargets = (await fetchSuggestedTargets()).targets ?? [];
    } catch {
      suggestedTargets = [];
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
      error = humanApiMessage(e, "saving the profile failed");
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
      const t = $state.snapshot(newTarget);
      const idx = Object.fromEntries(
        newIndexes.map((r) => [r.index.trim(), r.cql.trim()]).filter(([k, v]) => k && v),
      );
      t.indexes = Object.keys(idx).length > 0 ? idx : undefined;
      await putCopycatTarget(t);
      newTarget = { name: "", url: "", protocol: "sru" };
      newIndexes = [];
      await loadTargets();
    } catch (e) {
      error = humanApiMessage(e, "saving the target failed");
    }
  }

  /** Suggested presets not yet configured -- suppressed when a target already
   *  has that name OR that URL, so the good seeded k10plus-sru hides the k10plus
   * preset that points at the same server. */
  const suggestions = $derived(
    suggestedTargets
      .filter((s) => !targets.some((t) => t.name === s.name || t.url === s.url))
      .map((s) => ({ ...s, blurb: SUGGESTED_BLURBS[s.name] ?? s.name })),
  );

  async function addSuggested(s: CopycatTarget): Promise<void> {
    error = "";
    try {
      // Forward the whole target -- version, schema, and indexes included -- so a
      // preset with SRU knobs keeps them. blurb is UI-only.
      await putCopycatTarget({ name: s.name, url: s.url, protocol: s.protocol, version: s.version, schema: s.schema, indexes: s.indexes });
      await loadTargets();
    } catch (e) {
      error = humanApiMessage(e, "saving the target failed");
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
      st.warnings = res.warnings ?? {};
    } catch (e) {
      error = humanApiMessage(e, "search failed");
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
      error = humanApiMessage(e, "staging failed");
    } finally {
      busy = false;
    }
  }

  async function quickAdd(): Promise<void> {
    if (busy) return; // a scanner's trailing Enter must not re-enter mid-stage
    const isbn = normalizeIsbn(quickIsbn);
    if (!isbn) {
      error = "enter a 10- or 13-digit ISBN";
      return;
    }
    busy = true;
    error = "";
    status = "";
    st.results = [];
    st.picked = {};
    st.resultsSelected = 0;
    try {
      const res = await copycatSearch("", [{ index: "isbn", term: isbn }], profile?.targets ?? undefined);
      st.results = res.results ?? [];
      st.failures = res.failures ?? {};
      st.warnings = res.warnings ?? {};
      if (st.results.length === 0) {
        status = `no match for ${isbn}`;
        return;
      }
      if (!quickAutoStage || readOnly) {
        status = `${st.results.length} match${st.results.length === 1 ? "" : "es"} for ${isbn}`;
        return;
      }
      const staged = await stageCopycatBatch({
        label: `quick-add: ${isbn}`,
        source: "search",
        records: [$state.snapshot(st.results[0].record)],
        ...(profile?.policy ? { policy: profile.policy } : {}),
      });
      st.results = [];
      quickIsbn = "";
      await loadBatches();
      await open(staged.batch.id);
    } catch (e) {
      error = humanApiMessage(e, "quick-add failed");
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
      error = humanApiMessage(e, "upload failed");
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
    } catch (e) {
      // A COMMITTED batch is refused server-side (409): its revert-set is the
      // only undo for the works it created. Surface that reason.
      error = humanApiMessage(e, "deleting the batch failed");
    }
  }
</script>

<main class="wide" id="main" tabindex="-1">
  <h1>Copy cataloging</h1>

  <section class="quickadd" aria-label="Quick-add by ISBN">
    <label class="qa-field">
      <span class="muted">Quick-add ISBN</span>
      <!-- svelte-ignore a11y_autofocus -- a scan/paste bar earns focus on landing -->
      <input
        class="grow"
        inputmode="numeric"
        enterkeyhint="go"
        autofocus
        aria-label="Scan or paste an ISBN"
        placeholder="scan or paste an ISBN…"
        bind:value={quickIsbn}
        onkeydown={(ev) => ev.key === "Enter" && void quickAdd()}
      />
    </label>
    {#if !readOnly}
      <label class="pick"><input type="checkbox" bind:checked={quickAutoStage} /> auto-stage best match</label>
    {/if}
    <button class="button" onclick={() => void quickAdd()} disabled={busy || quickIsbn.trim() === ""}>Add</button>
  </section>

  <details class="targets">
    <summary>Search targets ({targets.length})</summary>
    <ul class="tlist">
      {#each targets as t (t.name)}
        <li>
          <span class="mono">{t.name}</span> · {t.protocol} · <span class="muted">{t.url}</span>
          {#if isAdmin && !readOnly}
            <button class="button button--quiet mini" onclick={() => void removeTarget(t.name)}>Remove</button>
          {/if}
        </li>
      {:else}
        <li class="muted">No targets configured{isAdmin ? "" : " -- ask an admin"}.</li>
      {/each}
    </ul>
    {#if isAdmin && !readOnly}
      <div class="row">
        <input aria-label="Target name" bind:value={newTarget.name} placeholder="name (e.g. loc)" />
        <input class="grow" aria-label="Target URL" bind:value={newTarget.url} placeholder="SRU base URL or z3950 host:port/DB" />
        <select aria-label="Protocol" bind:value={newTarget.protocol}>
          <option value="sru">SRU</option>
          <option value="z3950">Z39.50</option>
        </select>
        <button class="button" onclick={() => void addTarget()}>Add target</button>
      </div>
      {#if newTarget.protocol === "sru"}
        <details class="advanced-sru">
          <summary>Advanced (SRU version, schema, index overrides)</summary>
          <div class="row">
            <input aria-label="SRU version" bind:value={newTarget.version} placeholder="version (e.g. 1.1)" />
            <input aria-label="SRU record schema" bind:value={newTarget.schema} placeholder="schema (e.g. MARC21-xml)" />
          </div>
          <div class="idx-list">
            <span class="muted">Index overrides -- access point &rarr; CQL index (e.g. isbn &rarr; pica.isb for K10plus):</span>
            {#each newIndexes as row, i (i)}
              <div class="row">
                <input aria-label="Access point" bind:value={row.index} placeholder="isbn" />
                <span aria-hidden="true">&rarr;</span>
                <input aria-label="CQL index" bind:value={row.cql} placeholder="pica.isb" />
                <button class="button button--quiet mini" aria-label="Remove index override" onclick={() => (newIndexes = newIndexes.filter((_, j) => j !== i))}>&#10005;</button>
              </div>
            {/each}
            <button class="button button--quiet mini" onclick={() => (newIndexes = [...newIndexes, { index: "", cql: "" }])}>+ index override</button>
          </div>
        </details>
      {/if}
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
          {#if !readOnly}
            <button class="button button--quiet mini" onclick={() => void removeProfile(p.name)}>Remove</button>
          {/if}
        </li>
      {:else}
        <li class="muted">No profiles saved. A profile remembers target choices and overlay policy for recurring imports.</li>
      {/each}
    </ul>
    {#if !readOnly}
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
      {#if !readOnly}
        <label class="button button--quiet upload-btn">
          Stage a .mrc file… <input type="file" accept=".mrc,.marc" onchange={(ev) => void upload(ev)} hidden />
        </label>
        <a class="button button--quiet" href="#/copycat/new">New record…</a>
      {/if}
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
      {#each Object.entries(st.warnings) as [name, msg] (name)}
        <span class="warn">{name}: {msg} -- this target's results are incomplete</span>
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
            {#if !readOnly}
              <button
                class="button button--quiet mini"
                disabled={b.status === "COMMITTED"}
                title={b.status === "COMMITTED" ? "Revert this batch before deleting it -- its revert history is the only undo for the works it imported" : "Delete this batch"}
                onclick={() => void removeBatch(b.id)}>Delete</button>
            {/if}
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
  .quickadd {
    display: flex;
    gap: 0.8rem;
    align-items: end;
    flex-wrap: wrap;
    border: 1px solid var(--rule);
    border-radius: 8px;
    padding: 0.7rem 0.9rem;
    margin: 0.4rem 0 1rem;
  }
  .qa-field {
    display: flex;
    flex-direction: column;
    gap: 0.2rem;
    flex: 1;
    min-width: 16rem;
    font-size: 0.85rem;
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
  /* A target that answered incompletely is not a failed target: amber, not the
     danger red its hits would be filed under otherwise. */
  .warn {
    color: var(--pend-ink);
  }
</style>
