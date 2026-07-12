<script lang="ts">
  // The enrichment admin screen: configured sources with scoped run
  // controls, and the async job board. A corpus-scale pass (SRU harvest,
  // wikidata demographics) runs as a JOB -- kick returns immediately, a
  // worker executes, and this screen polls the job records for live batch
  // counters until every job reaches a terminal status. Suggestions land in
  // the moderation queue, never directly on records; a finished job links
  // straight to reviewing its output.
  import { onMount } from "svelte";
  import {
    createEnrichJob,
    fetchEnrichJobs,
    fetchEnrichSources,
    humanApiMessage,
    runEnrichSource,
  } from "../lib/api";
  import type { EnrichJob, EnrichRunResult } from "../lib/types";

  const POLL_MS = 3000;

  let sources = $state<string[]>([]);
  let jobs = $state<EnrichJob[]>([]);
  let source = $state("");
  let scope = $state("");
  let running = $state(false);
  let kicking = $state(false);
  let syncResult = $state<EnrichRunResult | null>(null);
  let loading = $state(true);
  let error = $state("");
  let timer: ReturnType<typeof setTimeout> | undefined;

  function filters(): string[] {
    return scope
      .split(/\s+/)
      .map((t) => t.trim())
      .filter(Boolean);
  }

  async function load(): Promise<void> {
    try {
      const [s, j] = await Promise.all([fetchEnrichSources(), fetchEnrichJobs()]);
      sources = (s.sources ?? []).sort();
      if (!source && sources.length > 0) source = sources[0];
      jobs = j.jobs ?? [];
      error = "";
    } catch (e) {
      error = humanApiMessage(e, "loading enrichment failed");
    } finally {
      loading = false;
    }
    schedule();
  }

  /** Poll while anything is live; stop on an all-terminal board. */
  function schedule(): void {
    clearTimeout(timer);
    if (jobs.some((j) => j.status === "QUEUED" || j.status === "RUNNING")) {
      timer = setTimeout(() => void load(), POLL_MS);
    }
  }

  onMount(() => {
    void load();
    return () => clearTimeout(timer);
  });

  async function kickJob(): Promise<void> {
    kicking = true;
    error = "";
    syncResult = null;
    try {
      await createEnrichJob(source, filters());
      await load();
    } catch (e) {
      error = humanApiMessage(e, "queuing the job failed");
    } finally {
      kicking = false;
    }
  }

  async function runSync(): Promise<void> {
    running = true;
    error = "";
    syncResult = null;
    try {
      syncResult = await runEnrichSource(source, filters());
    } catch (e) {
      error = humanApiMessage(e, "the run failed");
    } finally {
      running = false;
    }
  }

  function elapsed(j: EnrichJob): string {
    const ms = j.stats?.elapsedMs ?? 0;
    if (ms <= 0) return "";
    const s = Math.round(ms / 1000);
    if (s < 90) return `${s}s`;
    return `${Math.floor(s / 60)}m${String(s % 60).padStart(2, "0")}s`;
  }

  function scopeOf(j: EnrichJob): string {
    return (j.filters ?? []).map(([k, v]) => `${k}=${v}`).join(" ");
  }

  function when(iso?: string): string {
    if (!iso) return "";
    const d = new Date(iso);
    return isNaN(d.getTime()) ? "" : d.toLocaleString();
  }

  /** batches/total in [0,1] when the source sized its run up front; null
   *  keeps the indeterminate pulse for lazily-sized sources. */
  function fraction(j: EnrichJob): number | null {
    const total = j.stats?.total ?? 0;
    if (total <= 0) return null;
    return Math.min(1, (j.stats?.batches ?? 0) / total);
  }

  /** Coarse remaining-time estimate from the pace so far. */
  function eta(j: EnrichJob): string {
    const total = j.stats?.total ?? 0;
    const done = j.stats?.batches ?? 0;
    const ms = j.stats?.elapsedMs ?? 0;
    if (j.status !== "RUNNING" || total <= 0 || done <= 0 || done >= total || ms <= 0) return "";
    const left = Math.round(((ms / done) * (total - done)) / 60000);
    return left < 1 ? "under a minute left" : `~${left}m left`;
  }
</script>

<main class="enrichment" id="main" tabindex="-1">
  <header>
    <h1>Enrichment</h1>
    <p class="muted">
      Configured sources run over the corpus (or a <code>key=value</code> scope) and queue
      moderated suggestions -- nothing writes to records without review. Long runs belong
      in a job: kick returns immediately and progress shows below.
    </p>
  </header>

  {#if error}
    <p class="error" role="alert">{error}</p>
  {/if}

  {#if loading}
    <p class="muted">Loading…</p>
  {:else}
    <section class="run" aria-label="Run a source">
      <h2>Run a source</h2>
      <form
        onsubmit={(e) => {
          e.preventDefault();
          void kickJob();
        }}
      >
        <label for="enr-source">Source</label>
        <select id="enr-source" bind:value={source}>
          {#each sources as s (s)}
            <option value={s}>{s}</option>
          {/each}
        </select>
        <label for="enr-scope">Scope</label>
        <input id="enr-scope" bind:value={scope} placeholder="key=value key2=value2 (empty = whole corpus)" />
        <button type="submit" class="button" disabled={kicking || running || !source}>
          {kicking ? "Queuing…" : "Run as job"}
        </button>
        <button type="button" class="button button--quiet" onclick={() => void runSync()} disabled={kicking || running || !source}>
          {running ? "Running…" : "Run now (small scopes)"}
        </button>
      </form>
      <p class="muted hint">
        "Run as job" queues for the background worker -- the right choice for SRU or
        wikidata passes that take minutes to hours. "Run now" holds the request open and
        suits a narrow scope only.
      </p>
      {#if syncResult}
        <p class="ok" role="status">
          {syncResult.source}: {syncResult.works} work{syncResult.works === 1 ? "" : "s"}
          {syncResult.mode === "queue" ? "with suggestions queued" : "enriched"}
          {#if syncResult.stats}({syncResult.stats.batches} batches, {Math.round(syncResult.stats.elapsedMs / 1000)}s){/if}
          {#if syncResult.mode === "queue"}
            · <a href="#/queue?provenance=PIPELINE">review</a>
          {/if}
        </p>
      {/if}
    </section>

    <section class="jobs" aria-label="Jobs">
      <h2>Jobs</h2>
      {#if jobs.length === 0}
        <p class="muted">No jobs yet. Records expire a week after they finish.</p>
      {:else}
        <ul class="joblist">
          {#each jobs as j (j.id)}
            <li class={`job s-${j.status.toLowerCase()}`}>
              <div class="head">
                <span class="src">{j.source}</span>
                {#if scopeOf(j)}<span class="scope">{scopeOf(j)}</span>{/if}
                <span class={`status s-${j.status.toLowerCase()}`}>{j.status}</span>
                <span class="muted meta">
                  {j.requester} · queued {when(j.createdAt)}{#if j.startedAt}
                    · started {when(j.startedAt)}{/if}
                </span>
              </div>
              {#if j.status === "RUNNING" || j.status === "QUEUED"}
                {@const f = fraction(j)}
                {#if f !== null && j.status === "RUNNING"}
                  <!-- A source that sized its run up front gets a real
                       fraction; the pulse stays for lazily-sized sources. -->
                  <div class="bar" aria-hidden="true"><div class="fill" style="width: {Math.round(f * 1000) / 10}%"></div></div>
                {:else}
                  <div class="bar" class:waiting={j.status === "QUEUED"} aria-hidden="true"><div class="pulse"></div></div>
                {/if}
              {/if}
              <div class="stats">
                {#if j.stats}
                  {#if (j.stats.total ?? 0) > 0}
                    <span>{Math.round((fraction(j) ?? 0) * 100)}% · {j.stats.batches}/{j.stats.total}</span>
                  {:else}
                    <span>{j.stats.batches} batch{j.stats.batches === 1 ? "" : "es"}</span>
                  {/if}
                  {#if j.stats.skippedBatches}<span>{j.stats.skippedBatches} skipped</span>{/if}
                  {#if j.stats.resolvedCreators}<span>{j.stats.resolvedCreators} creators</span>{/if}
                  {#if j.stats.claims}<span>{j.stats.claims} claims</span>{/if}
                  {#if elapsed(j)}<span>{elapsed(j)}</span>{/if}
                  {#if eta(j)}<span>{eta(j)}</span>{/if}
                {:else if j.status === "QUEUED"}
                  <span class="muted">waiting for the worker…</span>
                {/if}
                {#if j.status === "DONE" && j.result}
                  <span class="ok">
                    {j.result.works} work{j.result.works === 1 ? "" : "s"}
                    {j.result.mode === "queue" ? "with suggestions queued" : "enriched"}
                  </span>
                  {#if j.result.mode === "queue"}
                    <a href="#/queue?provenance=PIPELINE">Review suggestions</a>
                  {/if}
                {/if}
                {#if j.status === "FAILED"}
                  <span class="error">{j.error || "failed"}</span>
                {/if}
              </div>
            </li>
          {/each}
        </ul>
      {/if}
    </section>
  {/if}
</main>

<style>
  .enrichment {
    max-width: 56rem;
    display: grid;
    gap: 1.25rem;
  }
  h1 {
    margin: 0 0 0.25rem;
  }
  h2 {
    margin: 0 0 0.5rem;
    font-size: 1.1rem;
  }
  .muted {
    color: var(--ink-muted, #667);
  }
  .hint {
    font-size: 0.82rem;
  }
  .error {
    color: var(--danger, #b00020);
  }
  .ok {
    color: var(--ok-ink, #1a6b32);
  }
  .run form {
    display: flex;
    gap: 0.5rem;
    align-items: center;
    flex-wrap: wrap;
  }
  #enr-scope {
    flex: 1;
    min-width: 16rem;
  }
  .joblist {
    list-style: none;
    margin: 0;
    padding: 0;
    display: grid;
    gap: 0.6rem;
  }
  .job {
    border: 1px solid var(--rule, #dde);
    border-radius: 8px;
    padding: 0.55rem 0.8rem;
    display: grid;
    gap: 0.35rem;
  }
  .job .head {
    display: flex;
    gap: 0.6rem;
    align-items: baseline;
    flex-wrap: wrap;
  }
  .src {
    font-weight: 600;
  }
  .scope {
    font-family: var(--mono, ui-monospace, monospace);
    font-size: 0.8rem;
    color: var(--ink-muted, #667);
  }
  .status {
    font-size: 0.68rem;
    font-weight: 650;
    text-transform: uppercase;
    letter-spacing: 0.05em;
    border-radius: 999px;
    padding: 0.1em 0.6em;
    border: 1px solid var(--rule, #dde);
  }
  .status.s-running {
    border-color: var(--accent, #4a7dff);
    color: var(--accent, #4a7dff);
  }
  .status.s-done {
    background: var(--tint-ok, #e2f4e6);
  }
  .status.s-failed {
    color: var(--danger, #b00020);
    border-color: var(--danger, #b00020);
  }
  .meta {
    margin-left: auto;
    font-size: 0.78rem;
  }
  /* Lazily-sized sources get the honest-indeterminate pulse: motion means
     alive, counters carry the truth. A source that knows its total up front
     (one search per driver term) renders the determinate .fill instead. */
  .bar {
    height: 4px;
    border-radius: 2px;
    background: var(--surface-alt, #eef0f4);
    overflow: hidden;
    position: relative;
  }
  .bar .pulse {
    position: absolute;
    inset: 0 auto 0 0;
    width: 30%;
    border-radius: 2px;
    background: var(--accent, #4a7dff);
    animation: slide 1.6s ease-in-out infinite;
  }
  .bar.waiting .pulse {
    animation-duration: 3.2s;
    opacity: 0.45;
  }
  .bar .fill {
    height: 100%;
    border-radius: 2px;
    background: var(--accent, #4a7dff);
    transition: width 0.6s ease;
  }
  @keyframes slide {
    0% {
      left: 0;
    }
    50% {
      left: 70%;
    }
    100% {
      left: 0;
    }
  }
  @media (prefers-reduced-motion: reduce) {
    .bar .pulse {
      animation: none;
      width: 100%;
      opacity: 0.35;
    }
  }
  .stats {
    display: flex;
    gap: 0.9rem;
    flex-wrap: wrap;
    font-size: 0.85rem;
    font-variant-numeric: tabular-nums;
  }
</style>
