<script lang="ts">
  // Batch operations (tasks/047): pick a selection (search, ids, saved
  // query, or the whole corpus), build an op list by hand or load a macro
  // (a shared macro run here is a modification template), always dry-run
  // first -- the exact quad deltas per work -- then execute with per-record
  // success/failure reporting.
  import { onMount } from "svelte";
  import {
    humanApiMessage,
    createSavedQuery,
    deleteSavedQuery,
    fetchMacros,
    fetchProfiles,
    fetchSavedQueries,
    postCoverBatch,
    resolveBatch,
    runBatch,
    type CoverBatchResponse,
  } from "../lib/api";
  import Modal from "../components/Modal.svelte";
  import { isReadOnly } from "../lib/config";
  import { ITEM_ACTIONS, ITEM_FIELDS, fieldKey, isItemField, itemOp, itemOpSummary, parseFieldKey, type ItemGuard } from "../lib/itemops";
  import { RESOURCE_ITEMS, type BatchRunResult, type BatchTarget, type Macro, type Op, type ProfileSummary, type SavedQuery, type Selection } from "../lib/types";

  // initialMacro preselects a macro (deep link #/batch?macro=<id>).
  let { initialMacro = "" }: { initialMacro?: string } = $props();

  const readOnly = isReadOnly();

  let coverZip = $state<File | null>(null);
  let coversBusy = $state(false);
  let coversError = $state("");
  let coversResult = $state<CoverBatchResponse | null>(null);

  /** Uploads the picked zip of covers keyed by workId/ISBN (tasks/220). */
  async function uploadCovers(): Promise<void> {
    if (!coverZip) return;
    coversBusy = true;
    coversError = "";
    coversResult = null;
    try {
      coversResult = await postCoverBatch(coverZip);
    } catch (e) {
      coversError = humanApiMessage(e, "cover upload failed");
    } finally {
      coversBusy = false;
    }
  }

  interface OpRow {
    /** "<resource>:<path>" -- work fields and item fields share one picker. */
    field: string;
    action: "add" | "remove" | "set" | "clear";
    value: string;
    lang: string;
    /** Item rows only: which copies the edit reaches. */
    guard: ItemGuard;
    where: string;
  }

  const emptyRow = (): OpRow => ({ field: "", action: "add", value: "", lang: "", guard: "all", where: "" });

  let kind = $state<Selection["kind"]>("search");
  let query = $state("");
  let idsText = $state("");
  let savedQueryId = $state("");
  let savedQueries = $state<SavedQuery[]>([]);
  let matched = $state<number | null>(null);
  let preview = $state<BatchTarget[]>([]);

  // A batch selection is query/id-defined, so it has no single work profile to
  // derive a field list from (tasks/346). Rather than hardcode work-monograph --
  // which offered the wrong fields for a work on fastadd -- let the cataloger pick
  // which work profile's fields the op-builder shows; default to work-monograph.
  let allProfiles = $state<Record<string, ProfileSummary>>({});
  let profileId = $state("work-monograph");
  const workProfile = $derived(allProfiles[profileId] ?? null);
  const workProfiles = $derived(
    Object.values(allProfiles)
      .filter((p) => p.resourceType === "work")
      .sort((a, b) => a.id.localeCompare(b.id)),
  );
  let macros = $state<Macro[]>([]);
  let macroId = $state("");
  let paramValues = $state<Record<string, string>>({});

  /** Params as sent: a cleared field is OMITTED, same as never touched --
   *  blank means "use the default", which is what the placeholder promises
   *  (tasks/231). The server treats "" the same way; this keeps the wire
   *  shape identical for both histories of the same blank field. */
  const sentParams = $derived(Object.fromEntries(Object.entries(paramValues).filter(([, v]) => v !== "")));
  let opRows = $state<OpRow[]>([emptyRow()]);

  let result = $state<BatchRunResult | null>(null);
  // The payload the last dry run previewed. Execute is enabled only while the
  // current inputs still serialize to it, so editing an op, macro, param, or
  // the selection after the dry run re-requires a fresh preview (tasks/113).
  let dryRunFor = $state("");
  let busy = $state(false);
  let error = $state("");
  let status = $state("");

  const macro = $derived(macros.find((m) => m.id === macroId) ?? null);
  const editableFields = $derived((workProfile?.fields ?? []).filter((f) => !f.hidden));

  onMount(() => {
    fetchProfiles().then(
      (r) => (allProfiles = r.profiles),
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

  /** Op rows as the wire sends them. An item row goes through itemOp, which
   *  refuses the shapes the server would: an item field holds one value, so
   *  only set and clear reach it. */
  function ops(): Op[] {
    const out: Op[] = [];
    for (const r of opRows) {
      const { resource, path } = parseFieldKey(r.field);
      if (!r.field) continue;
      if (resource === RESOURCE_ITEMS) {
        const op = itemOp(path, r.action, r.value, r.guard, r.where);
        if (op) out.push(op);
        continue;
      }
      if (r.action !== "clear" && !r.value) continue;
      const field = editableFields.find((f) => f.path === path);
      const k = field?.valueSource?.kind;
      const iri = k === "vocab" || k === "authority" || k === "entity";
      const v = { v: r.value, ...(r.lang ? { lang: r.lang } : {}), ...(iri ? { iri: true } : {}) };
      const op: Op = { resource: "work", path, action: r.action };
      if (r.action === "set") op.values = [v];
      else if (r.action !== "clear") op.value = v;
      out.push(op);
    }
    return out;
  }

  /** Plain-language reach of the staged item ops, shown before the dry run so
   *  "every copy in the catalog" is never a surprise. */
  const itemOpNotes = $derived(ops().filter((o) => o.resource === RESOURCE_ITEMS).map(itemOpSummary));

  /** Switching a row between a work field and an item field resets the action:
   *  "add" is legal on a work field and refused on an item one. */
  function onFieldChange(row: OpRow): void {
    if (isItemField(row.field) && !ITEM_ACTIONS.includes(row.action)) row.action = "set";
    if (!isItemField(row.field)) {
      row.guard = "all";
      row.where = "";
    }
  }

  /** The execute-relevant inputs, serialized; compared against `dryRunFor`. */
  const runPayload = $derived(
    JSON.stringify(macroId ? { selection: selection(), macroId, params: sentParams } : { selection: selection(), ops: ops() }),
  );
  const dryRunFresh = $derived(dryRunFor !== "" && dryRunFor === runPayload);

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
      error = humanApiMessage(e, "resolve failed");
    } finally {
      busy = false;
    }
  }

  async function run(dryRun: boolean): Promise<void> {
    busy = true;
    error = "";
    status = "";
    const payload = runPayload;
    try {
      const req = macroId
        ? { selection: selection(), macroId, params: sentParams, dryRun }
        : { selection: selection(), ops: ops(), dryRun };
      if (!macroId && (req.ops?.length ?? 0) === 0) {
        error = "no complete ops staged";
        return;
      }
      result = await runBatch(req);
      dryRunFor = dryRun ? payload : "";
      if (!dryRun) {
        status = `applied to ${result.applied} of ${result.matched} works` + (result.failed ? ` -- ${result.failed} failed` : "");
      }
    } catch (e) {
      result = null;
      error = humanApiMessage(e, "batch run failed");
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

<main class="wide" id="main" tabindex="-1">
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
        {#if !readOnly}
          <button class="button button--quiet" onclick={() => void saveQuery()} disabled={!query.trim()}>Save query…</button>
        {/if}
      {:else if kind === "savedQuery"}
        <select aria-label="Saved query" bind:value={savedQueryId}>
          <option value="">Pick a saved query…</option>
          {#each savedQueries as sq (sq.id)}
            <option value={sq.id}>{sq.label} ({sq.query})</option>
          {/each}
        </select>
        {#if savedQueryId && !readOnly}
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
      {#if workProfiles.length > 1}
        <div class="row">
          <label for="op-profile" class="muted">Fields from</label>
          <select id="op-profile" bind:value={profileId}>
            {#each workProfiles as p (p.id)}
              <option value={p.id}>{p.label || p.id}</option>
            {/each}
          </select>
          <span class="muted">which framework's fields to offer -- a batch can span profiles</span>
        </div>
      {/if}
      {#each opRows as row, i (i)}
        {@const items = isItemField(row.field)}
        <div class="row">
          <select aria-label="Field" bind:value={row.field} onchange={() => onFieldChange(row)}>
            <option value="">field…</option>
            <optgroup label="Work">
              {#each editableFields as f (f.path)}
                <option value={fieldKey("work", f.path)}>{f.label}</option>
              {/each}
            </optgroup>
            <optgroup label="Items (every copy)">
              {#each ITEM_FIELDS as f (f.path)}
                <option value={fieldKey(RESOURCE_ITEMS, f.path)}>{f.label}</option>
              {/each}
            </optgroup>
          </select>
          <select aria-label="Action" bind:value={row.action}>
            {#if items}
              <option value="set">set</option>
              <option value="clear">clear</option>
            {:else}
              <option value="add">add</option>
              <option value="remove">remove</option>
              <option value="set">set</option>
              <option value="clear">clear</option>
            {/if}
          </select>
          {#if row.action !== "clear"}
            <input class="grow" aria-label="Value" bind:value={row.value} placeholder="value (or ${'{'}param{'}'})" />
            {#if !items}
              <input class="lang" aria-label="Language" bind:value={row.lang} placeholder="lang" />
            {/if}
          {/if}
          <button class="button button--quiet" onclick={() => (opRows = opRows.filter((_, j) => j !== i))}>Remove</button>
        </div>
        {#if items}
          <div class="row guard">
            <select aria-label="Which items" bind:value={row.guard}>
              <option value="all">on every item</option>
              <option value="eq">only where the current value is</option>
              <option value="empty">only where it is empty</option>
            </select>
            {#if row.guard === "eq"}
              <input class="grow" aria-label="Current value" bind:value={row.where} placeholder="current value, e.g. Stacks" />
            {/if}
          </div>
        {/if}
      {/each}
      <button class="button button--quiet" onclick={() => (opRows = [...opRows, emptyRow()])}> Add op </button>
      {#if itemOpNotes.length > 0}
        <ul class="reach">
          {#each itemOpNotes as note (note)}
            <li>{note}</li>
          {/each}
        </ul>
      {/if}
    {/if}
  </section>

  <section aria-label="Run">
    <h2>3 · Run</h2>
    <p class="actions">
      <button class="button" onclick={() => void run(true)} disabled={busy}>Dry run</button>
      {#if !readOnly}
        <button class="button button--danger" onclick={() => void run(false)} disabled={busy || !dryRunFresh} title={dryRunFresh ? "" : "dry-run these exact inputs first"}>
          Execute
        </button>
      {/if}
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

  {#if !readOnly}
    <section aria-label="Batch covers">
      <h2>Batch covers</h2>
      <p class="muted">
        Upload a zip of cover images named <code>&lt;workId&gt;.jpg</code> or <code>&lt;isbn&gt;.png</code> (jpg/png/webp, 2MB each). ISBNs
        resolve against the catalog; hyphens don't matter.
      </p>
      <p class="actions">
        <input id="cover-zip" type="file" accept=".zip,application/zip" aria-label="Cover zip file" onchange={(ev) => (coverZip = (ev.currentTarget as HTMLInputElement).files?.[0] ?? null)} />
        <button class="button" onclick={() => void uploadCovers()} disabled={!coverZip || coversBusy}>Upload covers</button>
      </p>
      <p aria-live="polite">
        {#if coversBusy}<span class="muted">Uploading…</span>{/if}
        {#if coversError}<span class="error">{coversError}</span>{/if}
        {#if coversResult}
          <span class="ok">{coversResult.applied} cover{coversResult.applied === 1 ? "" : "s"} applied</span>
          <span class="muted"> · {coversResult.skipped} skipped</span>
          {#if coversResult.failed}<span class="error"> · {coversResult.failed} failed</span>{/if}
        {/if}
      </p>
      {#if coversResult}
        <ul class="results">
          {#each coversResult.results as item (item.file)}
            <li class:failed={!!item.skipped || !!item.failed}>
              <code>{item.file}</code>
              {#if item.failed}
                {#if item.workId}<a href={"#/works/" + encodeURIComponent(item.workId)}>{item.workId}</a>{/if}
                <span class="error">{item.failed}</span>
                {#if item.changed}
                  <strong class="error">the record was changed and needs fixing by hand</strong>
                {/if}
              {:else if item.skipped}
                <span class="muted">{item.skipped}</span>
              {:else}
                <a href={"#/works/" + encodeURIComponent(item.workId ?? "")}>{item.workId}</a>
                <span class="ok">applied</span>
              {/if}
            </li>
          {/each}
        </ul>
      {/if}
    </section>
  {/if}
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
  .row.guard {
    margin-left: 1.5rem;
    margin-top: -0.2rem;
  }
  .reach {
    margin: 0.6rem 0 0;
    padding-left: 1.1rem;
    color: var(--muted);
    font-size: 0.9rem;
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
