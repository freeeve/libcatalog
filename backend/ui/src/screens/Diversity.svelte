<script lang="ts">
  // Diversity audit: the coverage-first content-diversity report over the
  // live work index, with its methodology and limits stated on the page --
  // an audit number without its denominator misleads, so the coverage gauge
  // leads, every share draws against its named base, and the trends section
  // only says what recorded snapshots can support.
  import { onMount } from "svelte";
  import { fetchDiversityAudit, fetchDiversitySnapshots, recordDiversitySnapshot, humanApiMessage } from "../lib/api";
  import type { DiversityReport, DiversitySnapshot } from "../lib/types";

  let { initialFilter = "" }: { initialFilter?: string } = $props();

  let report = $state<DiversityReport | null>(null);
  let snapshots = $state<DiversitySnapshot[]>([]);
  let loading = $state(true);
  let recording = $state(false);
  let error = $state("");
  // The scope input: space-separated key=value terms matched against work
  // extras (e.g. "inQll=true"), ANDed -- the endpoint's ?filter semantics.
  // svelte-ignore state_referenced_locally
  let filterText = $state(initialFilter);

  function filterTerms(): string[] {
    return filterText.split(/\s+/).filter((t) => t.includes("="));
  }

  async function load(): Promise<void> {
    loading = true;
    error = "";
    try {
      const terms = filterTerms();
      [report, snapshots] = await Promise.all([
        fetchDiversityAudit(terms),
        fetchDiversitySnapshots(terms).then((r) => r.snapshots ?? []),
      ]);
    } catch (e) {
      error = humanApiMessage(e, "diversity audit failed");
    } finally {
      loading = false;
    }
  }

  function apply(ev: SubmitEvent): void {
    ev.preventDefault();
    void load();
  }

  async function record(): Promise<void> {
    recording = true;
    error = "";
    try {
      await recordDiversitySnapshot(filterTerms());
      snapshots = (await fetchDiversitySnapshots(filterTerms())).snapshots ?? [];
    } catch (e) {
      error = humanApiMessage(e, "snapshot failed");
    } finally {
      recording = false;
    }
  }

  onMount(() => {
    void load();
  });

  function pct(x: number): string {
    return (x * 100).toFixed(1) + "%";
  }

  // Gauge: a donut whose stroke covers the coverage fraction.
  const GAUGE_R = 26;
  const GAUGE_C = 2 * Math.PI * GAUGE_R;

  // The exclusive covered-works decomposition, as whole-collection fractions
  // -- the one composition that sums, so it may stack.
  function decomposition(r: DiversityReport): { label: string; frac: number; cls: string }[] {
    const t = r.totalWorks || 1;
    const m = r.multiplicity ?? { uncategorized: 0, matchedOne: 0, matchedMulti: 0 };
    return [
      { label: "matches 2+ categories", frac: m.matchedMulti / t, cls: "b-multi" },
      { label: "matches 1 category", frac: m.matchedOne / t, cls: "b-one" },
      { label: "subjected, no category", frac: m.uncategorized / t, cls: "b-uncat" },
      { label: "no subjects or tags", frac: (r.totalWorks - r.coveredWorks) / t, cls: "b-uncov" },
    ];
  }

  // Trend geometry. Panels share one y scale so heights compare across
  // categories; x is snapshot order with first/last dates as the axis.
  const TREND_W = 130;
  const TREND_H = 36;

  function trendMax(): number {
    let max = 0.05;
    for (const s of snapshots) {
      for (const c of s.categories ?? []) {
        if (c.shareCovered > max) max = c.shareCovered;
      }
    }
    return max;
  }

  function polyline(values: number[], max: number): string {
    const n = values.length;
    if (n < 2) return "";
    return values
      .map((v, i) => `${((i / (n - 1)) * TREND_W).toFixed(1)},${(TREND_H - (v / max) * TREND_H).toFixed(1)}`)
      .join(" ");
  }

  function categorySeries(id: string): number[] {
    return snapshots.map((s) => (s.categories ?? []).find((c) => c.id === id)?.shareCovered ?? 0);
  }

  // The decomposition over time as stacked area paths (bottom-up cumulative);
  // returns one closed path per band, same order/classes as decomposition().
  const AREA_W = 320;
  const AREA_H = 90;

  function areaPaths(): { d: string; cls: string; label: string }[] {
    if (snapshots.length < 2) return [];
    const n = snapshots.length;
    const bands = snapshots.map((s) => decomposition(s).map((b) => b.frac));
    const labels = decomposition(snapshots[0]);
    const x = (i: number) => (i / (n - 1)) * AREA_W;
    const y = (f: number) => AREA_H - f * AREA_H;
    const out: { d: string; cls: string; label: string }[] = [];
    let below = snapshots.map(() => 0);
    for (let b = 0; b < labels.length; b++) {
      const tops = below.map((base, i) => base + bands[i][b]);
      const upper = tops.map((f, i) => `${x(i).toFixed(1)},${y(f).toFixed(1)}`);
      const lower = below.map((f, i) => `${x(i).toFixed(1)},${y(f).toFixed(1)}`).reverse();
      out.push({ d: `M${upper.join("L")}L${lower.join("L")}Z`, cls: labels[b].cls, label: labels[b].label });
      below = tops;
    }
    return out;
  }
</script>

<main class="diversity" id="main" tabindex="-1">
  <header class="head">
    <h1>Diversity audit</h1>
    <p class="muted">
      What the collection is <em>about</em>, from its subject headings and tags --
      not who created it. <a href="#/diversity/config">Configure the categories</a>.
    </p>
  </header>

  <form class="scope" onsubmit={apply}>
    <label for="div-scope">Scope</label>
    <input
      id="div-scope"
      type="text"
      placeholder="key=value extras, e.g. inQll=true"
      bind:value={filterText}
    />
    <button type="submit" disabled={loading}>Apply</button>
    <span class="muted hint">
      space-separated <code>key=value</code> terms over work extras; empty = whole corpus
    </span>
  </form>

  {#if loading}
    <p class="muted">Auditing…</p>
  {:else if error}
    <p class="error" role="alert">{error}</p>
  {:else if report}
    <div class="coverage" role="status">
      <svg class="gauge" viewBox="0 0 64 64" width="64" height="64" aria-label={`Coverage ${pct(report.coverage)}`}>
        <circle class="gauge-track" cx="32" cy="32" r={GAUGE_R} fill="none" stroke-width="8" />
        <circle
          class="gauge-fill"
          cx="32" cy="32" r={GAUGE_R} fill="none" stroke-width="8"
          stroke-dasharray={`${(report.coverage * GAUGE_C).toFixed(1)} ${GAUGE_C.toFixed(1)}`}
          transform="rotate(-90 32 32)"
        />
        <text class="gauge-num" x="32" y="36" text-anchor="middle">{Math.round(report.coverage * 100)}%</text>
      </svg>
      <div class="stat">
        <span class="num">{report.totalWorks.toLocaleString()}</span>
        <span class="lbl">works audited</span>
      </div>
      <div class="stat">
        <span class="num">{report.coveredWorks.toLocaleString()}</span>
        <span class="lbl">carry any subject or tag</span>
      </div>
    </div>
    <p class="scopeline muted">
      {report.input}{report.scope ? ` -- scope: ${report.scope}` : ""}. Category
      shares below are of the {report.coveredWorks.toLocaleString()} works that
      carry subjects; a low coverage means the audit speaks for only part of the
      collection.
    </p>

    <div class="strip" role="img" aria-label="Exclusive composition of the collection">
      {#each decomposition(report) as band (band.cls)}
        {#if band.frac > 0}
          <div class={`seg ${band.cls}`} style={`width:${(band.frac * 100).toFixed(2)}%`}></div>
        {/if}
      {/each}
    </div>
    <div class="legend muted">
      {#each decomposition(report) as band (band.cls)}
        <span><i class={`chip ${band.cls}`}></i>{band.label} ({pct(band.frac)})</span>
      {/each}
    </div>

    <table class="cats">
      <thead>
        <tr>
          <th scope="col">Category</th>
          <th scope="col" class="bar-col"><span class="visually-hidden">Share of subjected works</span></th>
          <th scope="col" class="n">Works</th>
          <th scope="col" class="n">% subj.</th>
          <th scope="col" class="n">% coll.</th>
        </tr>
      </thead>
      <tbody>
        {#each report.categories as c (c.id)}
          <tr>
            <th scope="row">{c.label}</th>
            <td class="bar-col" aria-hidden="true">
              <div class="bar-track">
                <div class="bar" style={`width:${(c.shareCovered * 100).toFixed(2)}%`}></div>
                <div class="tick" style={`left:${(c.shareTotal * 100).toFixed(2)}%`} title="% of whole collection"></div>
              </div>
            </td>
            <td class="n">{c.works.toLocaleString()}</td>
            <td class="n">{pct(c.shareCovered)}</td>
            <td class="n">{pct(c.shareTotal)}</td>
          </tr>
        {/each}
      </tbody>
    </table>

    <section class="trends" aria-label="Trends">
      <div class="trends-head">
        <h3>Over time</h3>
        <button type="button" onclick={record} disabled={recording}>
          {recording ? "Recording…" : "Record snapshot"}
        </button>
        <span class="muted hint">
          one snapshot per scope per day; record at meaningful moments (after
          weeding, after an acquisition cycle)
        </span>
      </div>
      {#if snapshots.length >= 2}
        <h4>Composition</h4>
        <svg class="area" viewBox={`0 0 ${AREA_W} ${AREA_H}`} preserveAspectRatio="none" role="img"
          aria-label="Exclusive composition over time">
          {#each areaPaths() as band (band.cls)}
            <path class={band.cls} d={band.d} />
          {/each}
        </svg>
        <div class="axis muted">
          <span>{snapshots[0].date}</span>
          <span>{snapshots[snapshots.length - 1].date}</span>
        </div>

        <h4>Per category (% of subjected works)</h4>
        <div class="multiples">
          {#each report.categories as c (c.id)}
            <figure class="panel">
              <figcaption>{c.label}</figcaption>
              <svg viewBox={`0 0 ${TREND_W} ${TREND_H}`} width={TREND_W} height={TREND_H} role="img"
                aria-label={`${c.label} trend`}>
                <polyline class="spark" points={polyline(categorySeries(c.id), trendMax())} fill="none" />
              </svg>
              <span class="last">{pct(categorySeries(c.id).at(-1) ?? 0)}</span>
            </figure>
          {/each}
        </div>
        <p class="muted hint">
          Panels share one scale. Read every trend beside the coverage
          composition above: a move that tracks cataloging depth is a coverage
          artifact, not collection change.
        </p>
      {:else}
        <p class="muted">
          {snapshots.length === 1
            ? "One snapshot recorded -- a second unlocks the trend charts."
            : "No snapshots yet for this scope. Recorded audits accumulate into trend charts here."}
        </p>
      {/if}
    </section>

    <section class="creators" aria-label="Creator audit">
      <h3>Creators</h3>
      {#if report.creators}
        <div class="coverage" role="status">
          <div class="stat">
            <span class="num">{(report.creators.matchRate * 100).toFixed(1)}%</span>
            <span class="lbl">
              works matched ({report.creators.matchedWorks.toLocaleString()} of
              {report.creators.totalWorks.toLocaleString()})
            </span>
          </div>
          <div class="stat">
            <span class="num">{report.creators.resolvedCreators.toLocaleString()}</span>
            <span class="lbl">distinct creators resolved</span>
          </div>
        </div>
        <p class="scopeline muted">
          Distributions are over distinct resolved creators. Only claims Wikidata
          states explicitly are counted; "not stated" is the honest remainder,
          never backfilled. No person is named here.
        </p>
        {#each report.creators.properties as prop (prop.property)}
          <h4>{prop.label}</h4>
          <table class="cats creator-table">
            <tbody>
              <tr class="muted">
                <th scope="row">Not stated</th>
                <td class="bar-col" aria-hidden="true">
                  <div class="bar-track">
                    <div class="bar bar-muted" style={`width:${((prop.unknown / (report.creators.resolvedCreators || 1)) * 100).toFixed(2)}%`}></div>
                  </div>
                </td>
                <td class="n">{prop.unknown.toLocaleString()}</td>
              </tr>
              {#each prop.values ?? [] as v (v.qid)}
                <tr>
                  <th scope="row">{v.label}</th>
                  <td class="bar-col" aria-hidden="true">
                    <div class="bar-track">
                      <div class="bar" style={`width:${((v.creators / (report.creators.resolvedCreators || 1)) * 100).toFixed(2)}%`}></div>
                    </div>
                  </td>
                  <td class="n">{v.creators.toLocaleString()}</td>
                </tr>
              {/each}
            </tbody>
          </table>
        {/each}
      {:else}
        <p class="muted">
          Creator audit not enabled: no cached creator claims in this corpus.
          Configure <code>LCATD_ENRICH_WIKIDATA=direct</code> and run the
          wikidata enrichment source; resolution uses cataloged identifiers
          only -- never names.
        </p>
      {/if}
    </section>

    <section class="method" aria-label="Methodology and limits">
      <h3>How to read this</h3>
      <ul>
        <li>
          A work counts toward a category when any of its subject headings or
          tags matches the category's vocabulary crosswalk -- by authority URI,
          by vocabulary scheme (every Homosaurus term counts as LGBTQIA+), or by
          keyword against the heading text.
        </li>
        <li>
          This measures <strong>content</strong>: what works are about. It says
          nothing about creator identity -- that is a separate, opt-in analysis
          with its own consent and provenance rules.
        </li>
        <li>
          Works with no subjects or tags cannot be categorized; they dilute
          coverage rather than silently vanishing. Improving cataloging depth
          changes these numbers as much as collection development does.
        </li>
        <li>
          The taxonomy is an editorial choice, not a universal truth; operators
          tune it with a crosswalk override file. A zero can mean a genuine gap
          or vocabulary the crosswalk does not yet know.
        </li>
        <li>
          Suppressed (unpublished) works are included -- they are held.
          Tombstoned works are excluded -- they are retired.
        </li>
      </ul>
    </section>
  {/if}
</main>

<style>
  .diversity {
    max-width: 46rem;
  }
  .head p {
    margin-top: 0.25rem;
  }
  .scope {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    margin: 0.75rem 0 0.25rem;
    flex-wrap: wrap;
  }
  .scope input {
    min-width: 18rem;
  }
  .hint {
    font-size: 0.8rem;
  }
  .coverage {
    display: flex;
    gap: 1.5rem;
    align-items: center;
    margin: 1rem 0 0.5rem;
  }
  .stat {
    display: flex;
    flex-direction: column;
  }
  .stat .num {
    font-size: 1.5rem;
    font-weight: 600;
    font-variant-numeric: tabular-nums;
  }
  .stat .lbl {
    color: var(--ink-muted, #667);
    font-size: 0.85rem;
  }
  .gauge-track {
    stroke: var(--rule, #dde);
  }
  .gauge-fill {
    stroke: var(--accent, #4a7dff);
    stroke-linecap: round;
  }
  .gauge-num {
    font-size: 0.9rem;
    font-weight: 600;
    fill: var(--ink, #223);
  }
  .scopeline {
    margin: 0 0 1rem;
    font-size: 0.85rem;
  }

  .strip {
    display: flex;
    height: 0.75rem;
    border-radius: var(--radius, 4px);
    overflow: hidden;
    background: var(--surface-alt, #f2f3f5);
  }
  .seg,
  .chip {
    display: inline-block;
  }
  .b-multi {
    background: var(--accent, #4a7dff);
  }
  .b-one {
    background: color-mix(in srgb, var(--accent, #4a7dff) 55%, transparent);
  }
  .b-uncat {
    background: color-mix(in srgb, var(--accent, #4a7dff) 22%, transparent);
  }
  .b-uncov {
    background: var(--rule, #dde);
  }
  .legend {
    display: flex;
    flex-wrap: wrap;
    gap: 0.35rem 1rem;
    font-size: 0.78rem;
    margin: 0.35rem 0 1.25rem;
  }
  .legend .chip {
    width: 0.7rem;
    height: 0.7rem;
    border-radius: 2px;
    margin-right: 0.3rem;
    vertical-align: -1px;
  }

  table.cats {
    border-collapse: collapse;
    width: 100%;
  }
  .cats th,
  .cats td {
    text-align: left;
    padding: 0.35rem 0.75rem 0.35rem 0;
    border-bottom: 1px solid var(--rule, #dde);
  }
  .cats td.n,
  .cats th.n {
    text-align: right;
    font-variant-numeric: tabular-nums;
    white-space: nowrap;
  }
  .cats tbody th {
    font-weight: 500;
  }
  .bar-col {
    width: 34%;
    min-width: 8rem;
  }
  .bar-track {
    position: relative;
    height: 0.7rem;
    background: var(--surface-alt, #f2f3f5);
    border-radius: 2px;
    overflow: hidden;
  }
  .bar {
    height: 100%;
    background: var(--accent, #4a7dff);
    border-radius: 2px;
  }
  .bar-muted {
    background: var(--rule, #ccd);
  }
  .tick {
    position: absolute;
    top: -1px;
    bottom: -1px;
    width: 2px;
    background: var(--ink, #223);
    opacity: 0.55;
  }

  .trends {
    margin-top: 1.75rem;
  }
  .trends-head {
    display: flex;
    align-items: center;
    gap: 0.75rem;
    flex-wrap: wrap;
  }
  .trends h4 {
    margin: 1rem 0 0.35rem;
    font-size: 0.95rem;
  }
  .area {
    width: 100%;
    height: 90px;
    border-radius: var(--radius, 4px);
    background: var(--surface-alt, #f2f3f5);
  }
  .area path {
    stroke: none;
  }
  .axis {
    display: flex;
    justify-content: space-between;
    font-size: 0.75rem;
  }
  .multiples {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(10.5rem, 1fr));
    gap: 0.75rem 1.25rem;
    margin-top: 0.25rem;
  }
  .panel {
    margin: 0;
  }
  .panel figcaption {
    font-size: 0.78rem;
    color: var(--ink-muted, #667);
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .spark {
    stroke: var(--accent, #4a7dff);
    stroke-width: 2;
  }
  .panel .last {
    font-size: 0.8rem;
    font-variant-numeric: tabular-nums;
    margin-left: 0.35rem;
  }

  .creators {
    margin-top: 1.75rem;
  }
  .creators h4 {
    margin: 1rem 0 0.25rem;
    font-size: 0.95rem;
  }
  .creator-table {
    max-width: 34rem;
  }
  .method {
    margin-top: 1.5rem;
    font-size: 0.9rem;
  }
  .method h3 {
    font-size: 1rem;
  }
  .method li {
    margin: 0.35rem 0;
  }
  .muted {
    color: var(--ink-muted, #667);
  }
  .error {
    color: var(--danger, #b00020);
  }
  .visually-hidden {
    position: absolute;
    width: 1px;
    height: 1px;
    overflow: hidden;
    clip: rect(0 0 0 0);
  }
</style>
