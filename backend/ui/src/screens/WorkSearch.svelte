<script lang="ts">
  // Debounced search over /v1/works with keyboard-navigable results:
  // RowList carries j/k/arrows and Enter-to-open; "/" refocuses the box.
  // Query, rows, and selection live in screenState so returning from an
  // editor lands on the same row; a stale list refetches in the background
  // and re-finds the selected work by id.
  import { onMount } from "svelte";
  import { fetchWorks, resolveTermURIs, ApiError, type WorkFilters } from "../lib/api";
  import { getConfig } from "../lib/config";
  import { bindKeys, pushScope, popScope } from "../lib/keyboard";
  import { navigate } from "../lib/router";
  import { screenState } from "../lib/screenState.svelte";
  import { sequencer } from "../lib/sequence";
  import { bestLabel } from "../lib/vocab";
  import RowList from "../components/RowList.svelte";
  import type { FacetCount, WorkSummary } from "../lib/types";

  const SCOPE = "works";
  const DEBOUNCE_MS = 250;
  const FRESH_MS = 60_000;
  const seq = sequencer();

  const st = screenState("works", () => ({
    q: "",
    works: [] as WorkSummary[],
    total: 0,
    matched: 0,
    selected: 0,
    loadedAt: 0,
    filters: {} as WorkFilters,
    facets: {} as Record<string, FacetCount[]>,
  }));

  // Facet rail copy (tasks/168): fixed groups get cataloger-shaped labels;
  // subject values are IRIs resolved to term labels below. The deployment's
  // extras dimensions (tasks/171, e.g. sources/provenance) follow, humanized
  // from their config key -- their values are the raw extras strings.
  const FACET_GROUPS: { key: string; title: string; label: (v: string) => string }[] = [
    { key: "visibility", title: "Visibility", label: (v) => v },
    { key: "holdings", title: "Holdings", label: (v) => ({ physical: "physical items", digital: "live availability", none: "no holdings" })[v] ?? v },
    { key: "needs", title: "Needs", label: (v) => ({ subjects: "missing subjects", contributors: "missing contributors", isbn: "missing ISBN" })[v] ?? v },
    { key: "subject", title: "Subject", label: (v) => subjectLabels[v] ?? v.split("/").pop() ?? v },
    { key: "tag", title: "Tag", label: (v) => v },
    ...(getConfig().extraFacets ?? []).map((key) => ({
      key,
      title: key.charAt(0).toUpperCase() + key.slice(1),
      label: (v: string) => v,
    })),
  ];

  let subjectLabels = $state<Record<string, string>>({});
  // Scheme code -> whether any resolved term of it carries skos:broader/
  // narrower edges. Drives the rail-group parenthetical (tasks/176): calling
  // a hierarchy-less vocabulary "SKOS" implied structure it does not show.
  let schemeHierarchy = $state<Record<string, boolean>>({});

  // Per-group type-to-filter over the rail's rendered values (tasks/174):
  // keyed by rail-group key, matched against display labels.
  let railFilter = $state<Record<string, string>>({});

  type RailGroup = { key: string; filterKey: string; title: string; label: (v: string) => string; counts: FacetCount[] };

  /** Names one subject vocabulary group so catalogers see what authority
      they're filtering by (tasks/174). The parenthetical says "SKOS" only
      when the scheme's terms actually carry hierarchy links; a flat
      authority reads "controlled vocabulary" instead (tasks/176). */
  function schemeTitle(scheme: string): string {
    if (!scheme) return "Subject";
    const kind = schemeHierarchy[scheme] ? "SKOS Vocabulary" : "Controlled Vocabulary";
    return scheme.charAt(0).toUpperCase() + scheme.slice(1) + ` (${kind})`;
  }

  // The rendered rail: FACET_GROUPS with the subject group split into one
  // group per vocabulary scheme (tasks/174). All subject groups filter
  // through the same `subject` query parameter -- only display grouping
  // changes; scheme order follows the server's count-ranked value order.
  const railGroups = $derived.by<RailGroup[]>(() => {
    const out: RailGroup[] = [];
    for (const g of FACET_GROUPS) {
      const counts = st.facets[g.key] ?? [];
      if (counts.length === 0) continue;
      if (g.key !== "subject") {
        out.push({ key: g.key, filterKey: g.key, title: g.title, label: g.label, counts });
        continue;
      }
      const bySch = new Map<string, FacetCount[]>();
      for (const fcv of counts) {
        const s = fcv.scheme ?? "";
        if (!bySch.has(s)) bySch.set(s, []);
        bySch.get(s)!.push(fcv);
      }
      for (const [scheme, list] of bySch) {
        out.push({ key: "subject:" + scheme, filterKey: "subject", title: schemeTitle(scheme), label: g.label, counts: list });
      }
    }
    return out;
  });

  /** Applies the group's type-to-filter to its values. */
  function visibleCounts(group: RailGroup): FacetCount[] {
    const q = (railFilter[group.key] ?? "").trim().toLowerCase();
    if (q === "") return group.counts;
    return group.counts.filter((fcv) => group.label(fcv.value).toLowerCase().includes(q) || filterActive(group.filterKey, fcv.value));
  }

  /** Resolves the subject facet's IRIs to display labels, best-effort. */
  async function labelSubjects(facets: Record<string, FacetCount[]>): Promise<void> {
    const iris = (facets.subject ?? []).map((f) => f.value).filter((v) => !(v in subjectLabels));
    if (iris.length === 0) return;
    try {
      const { terms } = await resolveTermURIs(iris);
      const next = { ...subjectLabels };
      const hier = { ...schemeHierarchy };
      for (const [iri, term] of Object.entries(terms ?? {})) {
        next[iri] = bestLabel(term);
        if (term.scheme && ((term.broader?.length ?? 0) > 0 || (term.narrower?.length ?? 0) > 0)) hier[term.scheme] = true;
      }
      subjectLabels = next;
      schemeHierarchy = hier;
    } catch {
      // IRIs render as their tail segment until a later resolve succeeds.
    }
  }

  function filterActive(group: string, value: string): boolean {
    return (st.filters[group] ?? []).includes(value);
  }

  function toggleFilter(group: string, value: string): void {
    const cur = st.filters[group] ?? [];
    const next = cur.includes(value) ? cur.filter((v) => v !== value) : [...cur, value];
    if (next.length === 0) {
      const rest = { ...st.filters };
      delete rest[group];
      st.filters = rest;
    } else {
      st.filters = { ...st.filters, [group]: next };
    }
    void search(st.q, false);
  }

  function clearFilters(): void {
    st.filters = {};
    void search(st.q, false);
  }

  const filtersActive = $derived(Object.values(st.filters).some((v) => v.length > 0));

  let error = $state("");
  let loading = $state(false);
  let timer: ReturnType<typeof setTimeout> | undefined;

  onMount(() => {
    pushScope(SCOPE);
    const unbind = bindKeys(SCOPE, {
      "/": { description: "focus the search box", legend: "search", handler: focusSearch },
      m: { description: "load more results", legend: "more", handler: () => void loadMore() },
    });
    // A fresh-enough list is reused without refetching, but the subject
    // label and scheme-hierarchy maps are component state and reset on
    // every mount -- re-resolve them from the persisted facets or the rail
    // shows raw term ids after returning from a work (tasks/191).
    if (Date.now() - st.loadedAt > FRESH_MS) void search(st.q, true);
    else void labelSubjects(st.facets);
    return () => {
      unbind();
      popScope(SCOPE);
      clearTimeout(timer);
    };
  });

  function onInput(): void {
    clearTimeout(timer);
    timer = setTimeout(() => void search(st.q, false), DEBOUNCE_MS);
  }

  /** Runs the search; a refresh keeps the selection pinned to the same
      work id, a new query starts back at the top. */
  async function search(query: string, refresh: boolean): Promise<void> {
    const t = seq.take();
    loading = true;
    error = "";
    const keepId = refresh ? st.works[st.selected]?.WorkID : undefined;
    try {
      const page = await fetchWorks(query, 50, 0, st.filters);
      if (t.stale) return;
      st.works = page.works ?? [];
      st.total = page.total;
      st.matched = page.matched ?? st.works.length;
      st.facets = page.facets ?? {};
      void labelSubjects(st.facets);
      st.loadedAt = Date.now();
      const found = keepId ? st.works.findIndex((w) => w.WorkID === keepId) : -1;
      st.selected = found >= 0 ? found : Math.min(st.selected, Math.max(0, st.works.length - 1));
      if (!refresh) st.selected = 0;
    } catch (e) {
      if (t.stale) return;
      st.works = [];
      error = e instanceof ApiError && e.status === 401 ? "session expired -- sign in again" : "search failed";
    } finally {
      if (!t.stale) loading = false;
    }
  }

  /** Appends the next window of matches; selection stays put. */
  async function loadMore(): Promise<void> {
    if (loading || st.works.length >= st.matched) return;
    const t = seq.take();
    loading = true;
    error = "";
    try {
      const page = await fetchWorks(st.q, 50, st.works.length, st.filters);
      if (t.stale) return;
      const seen = new Set(st.works.map((w) => w.WorkID));
      st.works = [...st.works, ...(page.works ?? []).filter((w) => !seen.has(w.WorkID))];
      st.total = page.total;
      st.matched = page.matched ?? st.matched;
      st.loadedAt = Date.now();
    } catch {
      if (t.stale) return;
      error = "loading more failed";
    } finally {
      if (!t.stale) loading = false;
    }
  }

  function open(w: WorkSummary): void {
    navigate(`/works/${encodeURIComponent(w.WorkID)}`);
  }

  function focusSearch(): void {
    document.getElementById("work-q")?.focus();
  }
</script>

<main class="wide">
  <h1>Work search</h1>
  <p class="lede">
    <label for="work-q" class="muted">Title, contributor, tag, ISBN, or id</label>
  </p>
  <input id="work-q" type="search" bind:value={st.q} oninput={onInput} placeholder="Search works…" autocomplete="off" />
  <p class="muted status" aria-live="polite">
    {#if loading && st.works.length === 0}Searching…{:else if error}<span class="error">{error}</span>{:else}{st.works.length} of {st.matched} matched · {st.total} in catalog{/if}
    {#if !error && st.works.length > 0}
      · <a href={st.q.trim() ? "#/exports?kind=search&q=" + encodeURIComponent(st.q.trim()) : "#/exports?kind=all"}>Export these results…</a>
    {/if}
    {#if filtersActive}
      · <button class="link-button" onclick={clearFilters}>Clear filters</button>
    {/if}
  </p>

  <div class="results-layout">
    <aside class="facet-rail" aria-label="Filter results">
      {#each railGroups as group (group.key)}
        <fieldset class="facet-group">
          <legend title={group.title}>{group.title}</legend>
          {#if group.counts.length > 10}
            <input type="search" class="facet-filter" placeholder="Filter…" aria-label={"Filter " + group.title} bind:value={railFilter[group.key]} />
          {/if}
          {#each visibleCounts(group) as fc (fc.value)}
            <label class="facet-value" title={group.filterKey === "subject" ? fc.value : undefined}>
              <input type="checkbox" checked={filterActive(group.filterKey, fc.value)} onchange={() => toggleFilter(group.filterKey, fc.value)} />
              <span class="facet-label">{group.label(fc.value)}</span>
              <span class="facet-count muted">{fc.count}</span>
            </label>
          {/each}
        </fieldset>
      {/each}
    </aside>

    <div class="results-list">
      <RowList items={st.works} bind:selected={st.selected} getKey={(w) => w.WorkID} ariaLabel="Search results" scope={SCOPE} itemName="result" onactivate={open}>
        {#snippet row(w: WorkSummary)}
          <a class="row-link" href={"#/works/" + encodeURIComponent(w.WorkID)} title={w.Tags?.length ? w.Tags.join(", ") : undefined}>
            <span class="title">{w.Title || "(untitled)"}</span>
            <span class="muted who">{w.Contributors?.join("; ") ?? ""}</span>
            <span class="flags">
              {#if w.Tombstoned}<span class="flag" data-kind="tombstoned" title="retired; public search redirects or serves gone">tombstoned</span>{/if}
              {#if w.Suppressed}<span class="flag" data-kind="suppressed" title="hidden from public projection and search">suppressed</span>{/if}
              {#if w.Withdrawn}<span class="flag" data-kind="withdrawn" title={"gone from its feed since " + w.Withdrawn + " (tasks/078)"}>withdrawn</span>{/if}
              {#if !w.Items && !w.HasAvailability && !w.Tombstoned}<span class="flag" data-kind="unheld" title="no items and no live-availability identifier">no holdings</span>{/if}
            </span>
            <span class="id">{w.WorkID}</span>
          </a>
        {/snippet}
      </RowList>

      {#if st.works.length < st.matched}
        <p><button class="button button--quiet" onclick={() => void loadMore()} disabled={loading}>Load more ({st.matched - st.works.length} left)</button></p>
      {/if}
    </div>
  </div>
</main>

<style>
  #work-q {
    width: 100%;
    max-width: 28rem;
    font-size: 1rem;
  }
  .results-layout {
    display: grid;
    grid-template-columns: 13rem 1fr;
    gap: 0 1.2rem;
    align-items: start;
  }
  @media (max-width: 52rem) {
    .results-layout {
      grid-template-columns: 1fr;
    }
  }
  .facet-rail {
    display: flex;
    flex-direction: column;
    gap: 0.7rem;
    font-size: var(--fs-meta);
  }
  .facet-group {
    border: 1px solid var(--rule);
    border-radius: 6px;
    padding: 0.35rem 0.55rem 0.5rem;
    margin: 0;
    /* fieldsets default to min-inline-size: min-content, so a long legend
       forces the box past the rail column and under the results list */
    min-inline-size: 0;
  }
  .facet-group legend {
    font-weight: 650;
    padding: 0 0.3em;
    color: var(--ink-muted);
    text-transform: uppercase;
    font-size: 0.68rem;
    letter-spacing: 0.04em;
    max-width: 100%;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .facet-value {
    display: flex;
    align-items: baseline;
    gap: 0.4em;
    padding: 0.08rem 0;
    cursor: pointer;
  }
  .facet-filter {
    width: 100%;
    margin: 0.15rem 0 0.3rem;
    font: inherit;
    font-size: 0.78rem;
  }
  .facet-label {
    flex: 1;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .facet-count {
    font-variant-numeric: tabular-nums;
    flex-shrink: 0;
  }
  .link-button {
    background: none;
    border: none;
    padding: 0;
    font: inherit;
    color: var(--accent);
    text-decoration: underline;
    cursor: pointer;
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
    grid-template-columns: minmax(12rem, auto) 1fr auto auto;
    gap: 0 0.9rem;
    align-items: baseline;
    padding: 0.22rem 0.55rem;
    text-decoration: none;
    color: inherit;
  }
  .flags {
    display: inline-flex;
    gap: 0.3rem;
  }
  .flag {
    font-size: 0.68rem;
    font-weight: 650;
    border: 1px solid var(--rule);
    border-radius: 999px;
    padding: 0.02em 0.55em;
    color: var(--ink-muted);
    white-space: nowrap;
  }
  .flag[data-kind="suppressed"],
  .flag[data-kind="tombstoned"] {
    border-color: var(--danger);
    color: var(--danger);
  }
  .flag[data-kind="withdrawn"] {
    border-color: #c77d0a;
    color: #c77d0a;
  }
  .title {
    font-weight: 600;
    color: var(--accent);
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .who {
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .id {
    font-family: var(--mono);
    font-size: var(--fs-meta);
    color: var(--ink-muted);
  }
</style>
