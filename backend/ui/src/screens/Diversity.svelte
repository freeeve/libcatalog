<script lang="ts">
  // Diversity audit: the coverage-first content-diversity report
  // over the live work index, with its methodology and limits stated on the
  // page -- an audit number without its denominator misleads, so the coverage
  // block leads and every share names its base.
  import { onMount } from "svelte";
  import { fetchDiversityAudit, humanApiMessage } from "../lib/api";
  import type { DiversityReport } from "../lib/types";

  let { initialFilter = "" }: { initialFilter?: string } = $props();

  let report = $state<DiversityReport | null>(null);
  let loading = $state(true);
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
      report = await fetchDiversityAudit(filterTerms());
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

  onMount(() => {
    void load();
  });

  function pct(x: number): string {
    return (x * 100).toFixed(1) + "%";
  }
</script>

<section class="diversity" aria-label="Diversity audit">
  <header class="head">
    <h2>Diversity audit</h2>
    <p class="muted">
      What the collection is <em>about</em>, from its subject headings and tags --
      not who created it.
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
      <div class="stat">
        <span class="num">{report.totalWorks.toLocaleString()}</span>
        <span class="lbl">works audited</span>
      </div>
      <div class="stat">
        <span class="num">{report.coveredWorks.toLocaleString()}</span>
        <span class="lbl">carry any subject or tag</span>
      </div>
      <div class="stat">
        <span class="num">{pct(report.coverage)}</span>
        <span class="lbl">coverage</span>
      </div>
    </div>
    <p class="scopeline muted">
      {report.input}{report.scope ? ` -- scope: ${report.scope}` : ""}. Category
      shares below are of the {report.coveredWorks.toLocaleString()} works that
      carry subjects; a low coverage means the audit speaks for only part of the
      collection.
    </p>

    <table class="cats">
      <thead>
        <tr><th scope="col">Category</th><th scope="col" class="n">Works</th><th scope="col" class="n">% of subjected</th><th scope="col" class="n">% of collection</th></tr>
      </thead>
      <tbody>
        {#each report.categories as c (c.id)}
          <tr>
            <th scope="row">{c.label}</th>
            <td class="n">{c.works.toLocaleString()}</td>
            <td class="n">{pct(c.shareCovered)}</td>
            <td class="n">{pct(c.shareTotal)}</td>
          </tr>
        {/each}
      </tbody>
    </table>

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
          <table class="cats">
            <tbody>
              <tr class="muted">
                <th scope="row">Not stated</th>
                <td class="n">{prop.unknown.toLocaleString()}</td>
              </tr>
              {#each prop.values ?? [] as v (v.qid)}
                <tr>
                  <th scope="row">{v.label}</th>
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
</section>

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
  .scope .hint {
    font-size: 0.8rem;
  }
  .coverage {
    display: flex;
    gap: 2rem;
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
    color: var(--muted, #667);
    font-size: 0.85rem;
  }
  .scopeline {
    margin: 0 0 1rem;
    font-size: 0.85rem;
  }
  table.cats {
    border-collapse: collapse;
    width: 100%;
  }
  .cats th,
  .cats td {
    text-align: left;
    padding: 0.35rem 0.75rem 0.35rem 0;
    border-bottom: 1px solid var(--border, #dde);
  }
  .cats td.n,
  .cats th.n {
    text-align: right;
    font-variant-numeric: tabular-nums;
  }
  .cats tbody th {
    font-weight: 500;
  }
  .creators {
    margin-top: 1.75rem;
  }
  .creators h4 {
    margin: 1rem 0 0.25rem;
    font-size: 0.95rem;
  }
  .creators table.cats {
    max-width: 28rem;
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
    color: var(--muted, #667);
  }
  .error {
    color: var(--error, #b00020);
  }
</style>
