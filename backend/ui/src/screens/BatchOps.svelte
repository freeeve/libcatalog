<script lang="ts">
  // Batch operations (tasks/047): pick a selection (search, ids, saved
  // query, or the whole corpus), build an op list by hand or load a macro
  // (a shared macro run here is a modification template), always dry-run
  // first -- the exact quad deltas per work -- then execute with per-record
  // success/failure reporting.
  import { onMount } from "svelte";
  import {
    ApiError,
    createSavedQuery,
    deleteSavedQuery,
    fetchMacros,
    fetchProfiles,
    fetchSavedQueries,
    resolveBatch,
    runBatch,
  } from "../lib/api";
  import Modal from "../components/Modal.svelte";
  import type { BatchRunResult, BatchTarget, Macro, Op, Profile, SavedQuery, Selection } from "../lib/types";

  // initialMacro preselects a macro (deep link #/batch?macro=<id>).
  let { initialMacro = "" }: { initialMacro?: string } = $props();

  interface OpRow {
    path: string;
    action: "add" | "remove" | "set" | "clear";
    value: string;
    lang: string;
  }

  let kind = $state<Selection["kind"]>("search");
  let query = $state("");
  let idsText = $state("");
  let savedQueryId = $state("");
  let savedQueries = $state<SavedQuery[]>([]);
  let matched = $state<number | null>(null);
  let preview = $state<BatchTarget[]>([]);

  let workProfile = $state<Profile | null>(null);
  let macros = $state<Macro[]>([]);
  let macroId = $state("");
  let paramValues = $state<Record<string, string>>({});
  let opRows = $state<OpRow[]>([{ path: "", action: "add", value: "", lang: "" }]);

  let result = $state<BatchRunResult | null>(null);
  let dryRunDone = $state(false);
  let busy = $state(false);
  let error = $state("");
  let status = $state("");

  const macro = $derived(macros.find((m) => m.id === macroId) ?? null);
  const editableFields = $derived((workProfile?.fields ?? []).filter((f) => !f.hidden));

  onMount(() => {
    fetchProfiles().then(
      (r) => (workProfile = r.profiles["work-monograph"] ?? null),
      () => {},
    );
    fetchMacros().then(
      (r) => {
        macros = r.macros ?? [];
        if (initialMacro && macros.some((m) => m.id === initialMacro)) macroId = initialMacro;
      },
      () => {},
    );
    void loadQueries();
  });

  async function loadQueries(): Promise<void> {
    try {
      savedQueries = (await fetchSavedQueries()).queries ?? [];
    } catch {
      savedQueries = [];
    }
  }

  function selection(): Selection {
    switch (kind) {
      case "ids":
        return { kind, ids: idsText.split(/[\s,]+/).filter(Boolean) };
      case "search":
        return { kind, query };
      case "savedQuery":
        return { kind, savedQueryId };
      default:
        return { kind: "all" };
    }
  }

  function ops(): Op[] {
    return opRows
      .filter((r) => r.path && (r.action === "clear" || r.value))
      .map((r) => {
        const field = editableFields.find((f) => f.path === r.path);
        const k = field?.valueSource?.kind;
        const iri = k === "vocab" || k === "authority" || k === "entity";
        const v = { v: r.value, ...(r.lang ? { lang: r.lang } : {}), ...(iri ? { iri: true } : {}) };
        const op: Op = { resource: "work", path: r.path, action: r.action };
        if (r.action === "set") op.values = [v];
        else if (r.action !== "clear") op.value = v;
        return op;
      });
  }

  async function resolve(): Promise<void> {
    busy = true;
    error = "";
    try {
      const res = await resolveBatch(selection());
      matched = res.matched;
      preview = res.works ?? [];
    } catch (e) {
      matched = null;
      preview = [];
      error = e instanceof ApiError ? e.message : "resolve failed";
    } finally {
      busy = false;
    }
  }

  async function run(dryRun: boolean): Promise<void> {
    busy = true;
    error = "";
    status = "";
    try {
      const req = macroId
        ? { selection: selection(), macroId, params: paramValues, dryRun }
        : { selection: selection(), ops: ops(), dryRun };
      if (!macroId && (req.ops?.length ?? 0) === 0) {
        error = "no complete ops staged";
        return;
      }
      result = await runBatch(req);
      dryRunDone = dryRun;
      if (!dryRun) {
        status = `applied to ${result.applied} of ${result.matched} works` + (result.failed ? ` -- ${result.failed} failed` : "");
        dryRunDone = false;
      }
    } catch (e) {
      result = null;
      error = e instanceof ApiError ? e.message : "batch run failed";
    } finally {
      busy = false;
    }
  }

  /** Deep link carrying the current selection to the Exports screen. */
  function exportLink(): string {
    const p = new URLSearchParams({ kind });
    if (kind === "search") p.set("q", query);
    else if (kind === "ids") p.set("ids", idsText.split(/[\s,]+/).filter(Boolean).join(","));
    else if (kind === "savedQuery") p.set("sq", savedQueryId);
    return "#/exports?" + p.toString();
  }

  let namingQuery = $state(false);
  let queryLabel = $state("");

  function saveQuery(): void {
    queryLabel = query;
    namingQuery = true;
  }

  async function confirmSaveQuery(): Promise<void> {
    const label = queryLabel.trim();
    if (!label) return;
    namingQuery = false;
    try {
      await createSavedQuery(label, query);
      await loadQueries();
      status = `saved query "${label}"`;
    } catch {
      error = "saving the query failed";
    }
  }

  async function removeQuery(id: string): Promise<void> {
    try {
      await deleteSavedQuery(id);
      if (savedQueryId === id) savedQueryId = "";
      await loadQueries();
    } catch {
      error = "deleting the query failed";
    }
  }
</script>

<main>
  <h1>Batch operations</h1>

  <section aria-label="Selection">
    <h2>1 · Select works</h2>
    <div class="row">
      <label for="sel-kind" class="muted">Selection</label>
      <select id="sel-kind" bind:value={kind} onchange={() => ((matched = null), (preview = []))}>
        <option value="search">Search</option>
        <option value="ids">Work ids</option>
        <option value="savedQuery">Saved query</option>
        <option value="all">Entire catalog</option>
      </select>
      {#if kind === "search"}
        <input class="grow" aria-label="Search query" bind:value={query} placeholder="title, contributor, tag, ISBN…" />
        <button class="button button--quiet" onclick={() => void saveQuery()} disabled={!query.trim()}>Save query…</button>
      {:else if kind === "savedQuery"}
        <select aria-label="Saved query" bind:value={savedQueryId}>
          <option value="">Pick a saved query…</option>
          {#each savedQueries as sq (sq.id)}
            <option value={sq.id}>{sq.label} ({sq.query})</option>
          {/each}
        </select>
        {#if savedQueryId}
          <button class="button button--quiet" onclick={() => void removeQuery(savedQueryId)}>Delete saved query</button>
        {/if}
      {/if}
      <button class="button" onclick={() => void resolve()} disabled={busy}>Preview selection</button>
    </div>
    {#if kind === "ids"}
      <textarea aria-label="Work ids" bind:value={idsText} rows="3" placeholder="wabc123… one per line or comma-separated"
      ></textarea>
    {/if}
    <p class="muted" aria-live="polite">
      {#if matched !== null}
        {matched} work{matched === 1 ? "" : "s"} selected{#if preview.length < matched}&nbsp;(showing first {preview.length}){/if}
        · <a href={exportLink()}>Export selection…</a>
      {/if}
    </p>
    {#if preview.length > 0}
      <ul class="preview">
        {#each preview as w (w.workId)}
          <li><a href={"#/works/" + encodeURIComponent(w.workId)}>{w.title || w.workId}</a> <span class="id">{w.workId}</span></li>
        {/each}
      </ul>
    {/if}
  </section>

  <section aria-label="Operations">
    <h2>2 · Operations</h2>
    <div class="row">
      <label for="macro-pick" class="muted">Macro</label>
      <select id="macro-pick" bind:value={macroId}>
        <option value="">Build ops by hand</option>
        {#each macros as m (m.id)}
          <option value={m.id}>{m.label}{m.shared ? " (shared)" : ""}</option>
        {/each}
      </select>
    </div>

    {#if macro}
      {#if (macro.params ?? []).length > 0}
        <div class="params">
          {#each macro.params ?? [] as p (p.name)}
            <div class="row">
              <label for={"param-" + p.name}>{p.label || p.name}</label>
              <input id={"param-" + p.name} class="grow" bind:value={paramValues[p.name]} placeholder={p.default || ""} />
            </div>
          {/each}
        </div>
      {/if}
      <details>
        <summary class="muted">Macro ops ({macro.ops.length})</summary>
        <pre>{JSON.stringify(macro.ops, null, 2)}</pre>
      </details>
    {:else}
      {#each opRows as row, i (i)}
        <div class="row">
          <select aria-label="Field" bind:value={row.path}>
            <option value="">field…</option>
            {#each editableFields as f (f.path)}
              <option value={f.path}>{f.label}</option>
            {/each}
          </select>
          <select aria-label="Action" bind:value={row.action}>
            <option value="add">add</option>
            <option value="remove">remove</option>
            <option value="set">set</option>
            <option value="clear">clear</option>
          </select>
          {#if row.action !== "clear"}
            <input class="grow" aria-label="Value" bind:value={row.value} placeholder="value (or ${'{'}param{'}'})" />
            <input class="lang" aria-label="Language" bind:value={row.lang} placeholder="lang" />
          {/if}
          <button class="button button--quiet" onclick={() => (opRows = opRows.filter((_, j) => j !== i))}>Remove</button>
        </div>
      {/each}
      <button class="button button--quiet" onclick={() => (opRows = [...opRows, { path: "", action: "add", value: "", lang: "" }])}>
        Add op
      </button>
    {/if}
  </section>

  <section aria-label="Run">
    <h2>3 · Run</h2>
    <p class="actions">
      <button class="button" onclick={() => void run(true)} disabled={busy}>Dry run</button>
      <button class="button button--danger" onclick={() => void run(false)} disabled={busy || !dryRunDone} title={dryRunDone ? "" : "dry-run first"}>
        Execute
      </button>
    </p>
    <p aria-live="polite">
      {#if busy}<span class="muted">Running…</span>{/if}
      {#if status}<span class="ok">{status}</span>{/if}
      {#if error}<span class="error">{error}</span>{/if}
    </p>

    {#if result}
      <p class="summary">
        <strong>{result.dryRun ? "Dry run" : "Executed"}:</strong>
        {result.matched} matched · {result.applied} applied · {result.failed} failed ·
        <span class="add">+{result.added}</span> / <span class="del">-{result.removed}</span> quads
        {#if result.diffsTruncated}<span class="muted">(per-work diffs shown for the first works only)</span>{/if}
      </p>
      <ul class="results">
        {#each result.results as item (item.workId)}
          <li class:failed={!!item.error}>
            <a href={"#/works/" + encodeURIComponent(item.workId)}>{item.workId}</a>
            {#if item.error}
              <span class="error">{item.error}</span>
            {:else if item.diff}
              <details>
                <summary>+{item.diff.added.length} / -{item.diff.removed.length}</summary>
                <pre>{[...item.diff.removed.map((l) => "- " + l), ...item.diff.added.map((l) => "+ " + l)].join("\n")}</pre>
              </details>
            {:else}
              <span class="ok">applied</span>
            {/if}
          </li>
        {/each}
      </ul>
    {/if}
  </section>
</main>

{#if namingQuery}
  <Modal ariaLabel="Save this search" onclose={() => (namingQuery = false)} width="26rem">
    <form
      onsubmit={(ev) => {
        ev.preventDefault();
        void confirmSaveQuery();
      }}
    >
      <label for="sq-label">Name this search</label>
      <input id="sq-label" type="text" data-autofocus bind:value={queryLabel} autocomplete="off" />
      <p class="sq-actions">
        <button type="button" class="button button--quiet" onclick={() => (namingQuery = false)}>Cancel</button>
        <button type="submit" class="button" disabled={!queryLabel.trim()}>Save query</button>
      </p>
    </form>
  </Modal>
{/if}

<style>
  #sq-label {
    width: 100%;
    margin-top: 0.3rem;
  }
  .sq-actions {
    display: flex;
    gap: 0.6rem;
    justify-content: flex-end;
    margin: 0.9rem 0 0;
  }
  h2 {
    font-size: 1rem;
    margin: 1.2rem 0 0.5rem;
  }
  .row {
    display: flex;
    gap: 0.5rem;
    align-items: center;
    flex-wrap: wrap;
    margin-bottom: 0.45rem;
  }
  .grow {
    flex: 1;
    min-width: 12rem;
    max-width: 30rem;
  }
  .lang {
    width: 4.5rem;
  }
  textarea {
    width: 100%;
    max-width: 34rem;
    font-family: var(--mono);
    font-size: 0.85rem;
  }
  .preview,
  .results {
    list-style: none;
    margin: 0.4rem 0;
    padding: 0;
    max-height: 18rem;
    overflow-y: auto;
    border: 1px solid var(--rule);
    border-radius: 6px;
  }
  .preview li,
  .results li {
    display: flex;
    gap: 0.7rem;
    align-items: baseline;
    padding: 0.3rem 0.6rem;
    border-bottom: 1px solid var(--rule);
    flex-wrap: wrap;
  }
  .results li.failed {
    background: color-mix(in srgb, var(--surface) 92%, var(--danger));
  }
  .id {
    font-family: var(--mono);
    font-size: 0.75rem;
    color: var(--ink-muted);
  }
  .params {
    border-left: 3px solid var(--accent);
    padding-left: 0.8rem;
    margin: 0.5rem 0;
  }
  pre {
    font-family: var(--mono);
    font-size: 0.75rem;
    background: var(--surface);
    border: 1px solid var(--rule);
    border-radius: 6px;
    padding: 0.5rem 0.7rem;
    overflow-x: auto;
    max-height: 14rem;
    overflow-y: auto;
  }
  .actions {
    display: flex;
    gap: 0.75rem;
  }
  .summary .add {
    color: var(--ok, green);
  }
  .summary .del {
    color: var(--danger);
  }
  .ok {
    color: var(--accent);
  }
</style>
