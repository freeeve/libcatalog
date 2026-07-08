<script lang="ts">
  // Exports (tasks/048): a new-export form (format + selection, honest about
  // MARC lossiness) over the tasks/038 job runner, and the job list with
  // live status, record counts, expiry, and download links. Search results
  // and the batch screen deep-link here with the selection prefilled
  // (#/exports?kind=search&q=…).
  import { onMount } from "svelte";
  import { ApiError, createAuthorityExport, createExport, fetchSavedQueries, resolveBatch, fetchExports } from "../lib/api";
  import { getConfig } from "../lib/config";
  import { bindKeys, popScope, pushScope } from "../lib/keyboard";
  import type { ExportFormat, ExportJob, SavedQuery, Selection } from "../lib/types";

  // Prefill from a deep link (kind + query/ids/savedQueryId).
  let {
    initialKind = "",
    initialQuery = "",
    initialIds = "",
    initialSavedQuery = "",
  }: { initialKind?: string; initialQuery?: string; initialIds?: string; initialSavedQuery?: string } = $props();

  const FORMATS: { value: ExportFormat; label: string; note: string }[] = [
    {
      value: "marc",
      label: "MARC (.mrc)",
      note: "Lossy: records travel BIBFRAME->MARC through the libcodex round-trip. See the fidelity table for exactly which fields survive.",
    },
    { value: "nquads", label: "BIBFRAME N-Quads (.nq)", note: "Lossless: the canonical grain statements, merged corpus-style." },
    { value: "jsonld", label: "JSON-LD (.jsonld)", note: "The record path's JSON-LD; fidelity-bounded like the MARC detour." },
    { value: "csv", label: "CSV (.csv)", note: "Projected rows: id, title, contributors, subjects, and friends." },
  ];
  const FIDELITY_URL = "https://github.com/freeeve/libcat/blob/main/docs/marc-fidelity.md";
  const POLL_MS = 4000;

  // Prefill props are deliberately consumed once at mount (the route keys a
  // fresh mount per deep link).
  // svelte-ignore state_referenced_locally
  let kind = $state<Selection["kind"]>(initialKind === "ids" || initialKind === "all" || initialKind === "savedQuery" ? initialKind : "search");
  // svelte-ignore state_referenced_locally
  let query = $state(initialQuery);
  // svelte-ignore state_referenced_locally
  let idsText = $state(initialIds);
  // svelte-ignore state_referenced_locally
  let savedQueryId = $state(initialSavedQuery);
  let savedQueries = $state<SavedQuery[]>([]);
  let format = $state<ExportFormat>("csv");
  // Authority exports (tasks/069): terms instead of work grains.
  let target = $state<"works" | "authorities">("works");
  let vocabPicks = $state<Record<string, boolean>>({});
  let labelFilter = $state("");
  const schemes = (getConfig().schemes ?? []).filter((s) => s !== "folk");
  let matched = $state<number | null>(null);
  let jobs = $state<ExportJob[]>([]);
  let selected = $state(0);
  let busy = $state(false);
  let error = $state("");
  let status = $state("");
  let timer: ReturnType<typeof setInterval> | undefined;

  const SCOPE = "exports";

  const formatNote = $derived(FORMATS.find((f) => f.value === format)?.note ?? "");
  const hasActive = $derived(jobs.some((j) => j.status === "QUEUED" || j.status === "RUNNING"));

  // CSV has no authority shape; switching targets steers off it.
  $effect(() => {
    if (target === "authorities" && format === "csv") format = "nquads";
  });

  onMount(() => {
    pushScope(SCOPE);
    const unbind = bindKeys(SCOPE, {
      j: { description: "next job", legend: "move", keyLabel: "j/k", handler: () => move(1) },
      k: { description: "previous job", hidden: true, handler: () => move(-1) },
      ArrowDown: { description: "next job", hidden: true, handler: () => move(1) },
      ArrowUp: { description: "previous job", hidden: true, handler: () => move(-1) },
      Enter: { description: "download the selected job", legend: "download", handler: downloadSelected },
      r: { description: "refresh the job list", legend: "refresh", handler: () => void refresh() },
      n: { description: "focus the new-export form", legend: "new export", handler: focusForm },
    });
    void refresh();
    fetchSavedQueries().then(
      (r) => (savedQueries = r.queries ?? []),
      () => {},
    );
    if (initialKind) void preview();
    timer = setInterval(() => {
      if (hasActive) void refresh();
    }, POLL_MS);
    return () => {
      unbind();
      popScope(SCOPE);
      clearInterval(timer);
    };
  });

  function move(delta: number): void {
    if (jobs.length === 0) return;
    selected = Math.min(jobs.length - 1, Math.max(0, selected + delta));
    document.querySelectorAll("tbody tr")[selected]?.scrollIntoView?.({ block: "nearest" });
  }

  function downloadSelected(): void {
    const j = jobs[selected];
    if (j && j.downloadUrl && !expired(j)) location.href = j.downloadUrl;
  }

  function focusForm(): void {
    document.getElementById("ex-kind")?.focus();
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

  async function preview(): Promise<void> {
    error = "";
    try {
      matched = (await resolveBatch(selection())).matched;
    } catch (e) {
      matched = null;
      error = e instanceof ApiError ? e.message : "selection preview failed";
    }
  }

  async function submit(): Promise<void> {
    busy = true;
    error = "";
    status = "";
    try {
      let job: ExportJob;
      if (target === "authorities") {
        const vocabs = Object.entries(vocabPicks)
          .filter(([, on]) => on)
          .map(([s]) => s);
        job = await createAuthorityExport(format, {
          ...(vocabs.length > 0 ? { vocabs } : { all: true }),
          ...(labelFilter.trim() ? { label: labelFilter.trim() } : {}),
        });
      } else {
        job = await createExport(format, selection());
      }
      status =
        job.status === "DONE"
          ? `export ready: ${job.records} record${job.records === 1 ? "" : "s"}`
          : "export queued -- the worker picks it up shortly";
      await refresh();
    } catch (e) {
      error = e instanceof ApiError ? e.message : "creating the export failed";
    } finally {
      busy = false;
    }
  }

  async function refresh(): Promise<void> {
    try {
      jobs = (await fetchExports()).exports ?? [];
    } catch {
      // transient; the poll retries
    }
  }

  function expired(j: ExportJob): boolean {
    return j.status === "DONE" && !!j.expiresAt && new Date(j.expiresAt).getTime() < Date.now();
  }

  function expiry(j: ExportJob): string {
    if (!j.expiresAt) return "";
    if (expired(j)) return "expired";
    const hours = Math.max(1, Math.round((new Date(j.expiresAt).getTime() - Date.now()) / 3_600_000));
    return `expires in ~${hours}h`;
  }

  function describeSelection(j: ExportJob): string {
    if (j.authorities) {
      const scope = j.authorities.vocabs?.length ? j.authorities.vocabs.join(", ") : "all vocabularies";
      return `authorities: ${scope}${j.authorities.label ? ` "${j.authorities.label}…"` : ""}`;
    }
    if (j.selection.all) return "entire catalog";
    return `${j.selection.workIds?.length ?? 0} works`;
  }
</script>

<main class="wide">
  <h1>Exports</h1>

  <section aria-label="New export">
    <h2>New export</h2>
    <div class="row">
      <label for="ex-target" class="muted">Export</label>
      <select id="ex-target" bind:value={target}>
        <option value="works">Works</option>
        <option value="authorities">Authorities</option>
      </select>
    </div>
    {#if target === "authorities"}
      <div class="row">
        <span class="muted">Vocabularies</span>
        {#each schemes as s (s)}
          <label class="pick"><input type="checkbox" bind:checked={vocabPicks[s]} /> {s}</label>
        {:else}
          <span class="muted">none loaded</span>
        {/each}
        <span class="muted">(none checked = all)</span>
      </div>
      <div class="row">
        <label for="ex-label" class="muted">Label filter</label>
        <input id="ex-label" class="grow" bind:value={labelFilter} placeholder="optional label prefix (runs immediately)" />
      </div>
    {:else}
    <div class="row">
      <label for="ex-kind" class="muted">Selection</label>
      <select id="ex-kind" bind:value={kind} onchange={() => (matched = null)}>
        <option value="search">Search</option>
        <option value="ids">Work ids</option>
        <option value="savedQuery">Saved query</option>
        <option value="all">Entire catalog</option>
      </select>
      {#if kind === "search"}
        <input class="grow" aria-label="Search query" bind:value={query} placeholder="title, contributor, tag, ISBN…" />
      {:else if kind === "savedQuery"}
        <select aria-label="Saved query" bind:value={savedQueryId}>
          <option value="">Pick a saved query…</option>
          {#each savedQueries as sq (sq.id)}
            <option value={sq.id}>{sq.label} ({sq.query})</option>
          {/each}
        </select>
      {/if}
      <button class="button button--quiet" onclick={() => void preview()}>Preview</button>
      <span class="muted" aria-live="polite">
        {#if matched !== null}{matched} work{matched === 1 ? "" : "s"}{/if}
      </span>
    </div>
    {#if kind === "ids"}
      <textarea aria-label="Work ids" bind:value={idsText} rows="3" placeholder="wabc123… one per line or comma-separated"
      ></textarea>
    {/if}
    {/if}

    <div class="row">
      <label for="ex-format" class="muted">Format</label>
      <select id="ex-format" bind:value={format}>
        {#each FORMATS.filter((f) => target !== "authorities" || f.value !== "csv") as f (f.value)}
          <option value={f.value}>{target === "authorities" && f.value === "marc" ? "MARC authority (.mrc)" : f.label}</option>
        {/each}
      </select>
    </div>
    <p class="note" class:warn={format === "marc"}>
      {formatNote}
      {#if format === "marc"}
        <a href={FIDELITY_URL} target="_blank" rel="noreferrer">MARC fidelity table</a>
      {/if}
    </p>

    <p class="actions">
      <button class="button" onclick={() => void submit()} disabled={busy}>Export</button>
      <span aria-live="polite">
        {#if status}<span class="ok">{status}</span>{/if}
        {#if error}<span class="error">{error}</span>{/if}
      </span>
    </p>
  </section>

  <section aria-label="Export jobs">
    <h2>Jobs</h2>
    {#if jobs.length === 0}
      <p class="muted">No exports yet.</p>
    {:else}
      <table>
        <thead>
          <tr><th scope="col">Created</th><th scope="col">Format</th><th scope="col">Selection</th><th scope="col">Status</th><th scope="col">Records</th><th scope="col">Download</th></tr>
        </thead>
        <tbody>
          {#each jobs as j, i (j.id)}
            <tr class:selected={i === selected} onfocusin={() => (selected = i)}>
              <td>{new Date(j.createdAt).toLocaleString()}</td>
              <td class="mono">{j.format}</td>
              <td>{describeSelection(j)}</td>
              <td>
                <span class="badge" data-status={expired(j) ? "EXPIRED" : j.status}>{expired(j) ? "EXPIRED" : j.status}</span>
                {#if j.error}<span class="error">{j.error}</span>{/if}
              </td>
              <td>{j.records ?? ""}</td>
              <td>
                {#if expired(j)}
                  <span class="muted">expired</span>
                {:else if j.downloadUrl}
                  <a href={j.downloadUrl}>download</a>
                  <span class="muted">{expiry(j)}</span>
                {:else if j.status === "QUEUED" || j.status === "RUNNING"}
                  <span class="muted">working…</span>
                {/if}
              </td>
            </tr>
          {/each}
        </tbody>
      </table>
    {/if}
  </section>
</main>

<style>
  h2 {
    font-size: 1rem;
    margin: 1.1rem 0 0.5rem;
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
    max-width: 28rem;
  }
  textarea {
    width: 100%;
    max-width: 34rem;
    font-family: var(--mono);
    font-size: 0.85rem;
  }
  .note {
    font-size: 0.87rem;
    color: var(--ink-muted);
    max-width: 42rem;
    border-left: 3px solid var(--rule);
    padding-left: 0.7rem;
  }
  .note.warn {
    border-left-color: #c77d0a;
    color: inherit;
  }
  .actions {
    display: flex;
    gap: 0.8rem;
    align-items: center;
  }
  table {
    border-collapse: collapse;
    width: 100%;
    font-size: 0.9rem;
  }
  th,
  td {
    text-align: left;
    padding: 0.35rem 0.6rem;
    border-bottom: 1px solid var(--rule);
  }
  tbody tr.selected {
    background: var(--surface);
  }
  tbody tr.selected td:first-child {
    box-shadow: inset 3px 0 0 var(--accent);
  }
  .mono {
    font-family: var(--mono);
    font-size: 0.82rem;
  }
  .badge {
    font-size: 0.72rem;
    font-weight: 700;
    border-radius: 999px;
    padding: 0.1em 0.7em;
    border: 1px solid var(--rule);
  }
  .badge[data-status="DONE"] {
    background: var(--accent);
    border-color: var(--accent);
    color: var(--accent-ink);
  }
  .badge[data-status="FAILED"] {
    background: var(--danger);
    border-color: var(--danger);
    color: #fff;
  }
  .badge[data-status="EXPIRED"] {
    color: var(--ink-muted);
  }
  .ok {
    color: var(--accent);
  }
</style>
