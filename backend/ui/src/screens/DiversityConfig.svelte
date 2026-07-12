<script lang="ts">
  // The diversity-crosswalk configuration screen. The centerpiece is the
  // facet builder: the corpus's actual subject terms as a work-count
  // histogram, so an operator picks the terms worth their own category from
  // what the collection holds instead of hand-typing authority URIs. Edits
  // persist as a TOML override (the same file `lcat diversity-audit
  // --crosswalk` reads) merged over the built-in seed: an override EXTENDS a
  // seed category (adds keywords/URIs/schemes, relabels) or appends new ones;
  // seed matching is never removable, so the built-in floor stays honest.
  import {
    deleteDiversityCrosswalk,
    fetchAuditTerms,
    fetchDiversityCrosswalk,
    humanApiMessage,
    previewDiversityCrosswalk,
    saveDiversityCrosswalk,
  } from "../lib/api";
  import type { AuditTerm, AuditTermsPage, CrosswalkCategory, CrosswalkView, DiversityReport } from "../lib/types";

  let { initialFilter = "" }: { initialFilter?: string } = $props();

  // draft is one override category as the form edits it: list fields as
  // joined text so typing never fights an array round-trip.
  interface Draft {
    id: string;
    label: string;
    keywords: string;
    uris: string;
    schemes: string;
  }

  let view = $state<CrosswalkView | null>(null);
  let drafts = $state<Draft[]>([]);
  let terms = $state<AuditTermsPage | null>(null);
  let preview = $state<DiversityReport | null>(null);
  // svelte-ignore state_referenced_locally -- the route query seeds the field once
  let scope = $state(initialFilter);
  let search = $state("");
  let target = $state(0);
  let checked = $state<Record<string, boolean>>({});
  let tomlDraft = $state("");
  let loading = $state(true);
  let busy = $state("");
  let error = $state("");
  let notice = $state("");

  function toDraft(c: CrosswalkCategory): Draft {
    return {
      id: c.id,
      label: c.label ?? "",
      keywords: (c.keywords ?? []).join(", "),
      uris: (c.uris ?? []).join("\n"),
      schemes: (c.schemes ?? []).join(", "),
    };
  }

  function split(s: string, sep: RegExp): string[] {
    return s
      .split(sep)
      .map((v) => v.trim())
      .filter(Boolean);
  }

  /** The drafts as the API's category model; empty list fields drop out. */
  function categories(): CrosswalkCategory[] {
    return drafts.map((d) => ({
      id: d.id.trim(),
      label: d.label.trim() || undefined,
      keywords: split(d.keywords, /,/).length ? split(d.keywords, /,/) : undefined,
      uris: split(d.uris, /\n/).length ? split(d.uris, /\n/) : undefined,
      schemes: split(d.schemes, /,/).length ? split(d.schemes, /,/) : undefined,
    }));
  }

  function applyView(v: CrosswalkView): void {
    view = v;
    drafts = (v.override ?? []).map(toDraft);
    tomlDraft = v.toml ?? "";
  }

  async function loadAll(): Promise<void> {
    loading = true;
    error = "";
    try {
      const filters = split(scope, /\s+/);
      const [v, t] = await Promise.all([fetchDiversityCrosswalk(), fetchAuditTerms(filters)]);
      applyView(v);
      terms = t;
    } catch (e) {
      error = humanApiMessage(e, "loading the crosswalk failed");
    } finally {
      loading = false;
    }
  }

  void loadAll();

  function addCategory(): void {
    drafts = [...drafts, { id: "", label: "", keywords: "", uris: "", schemes: "" }];
    target = drafts.length - 1;
  }

  /** Prefills an override entry for a seed category so it can be extended. */
  function extendSeed(id: string, label: string): void {
    const at = drafts.findIndex((d) => d.id === id);
    if (at >= 0) {
      target = at;
      return;
    }
    drafts = [...drafts, { id, label, keywords: "", uris: "", schemes: "" }];
    target = drafts.length - 1;
  }

  function removeCategory(i: number): void {
    drafts = drafts.filter((_, j) => j !== i);
    if (target >= drafts.length) target = Math.max(0, drafts.length - 1);
    preview = null;
  }

  function termKey(t: AuditTerm): string {
    return t.uri ?? `label:${t.label}`;
  }

  const selectedCount = $derived(Object.values(checked).filter(Boolean).length);

  /** Adds the checked histogram terms to the target category: URIs as exact
   *  URI matches, headings and tags as keywords. */
  function addSelected(): void {
    if (drafts.length === 0 || !terms) return;
    const d = drafts[target];
    const uris = new Set(split(d.uris, /\n/));
    const kws = new Set(split(d.keywords, /,/).map((k) => k.toLowerCase()));
    for (const list of [terms.uris, terms.headings, terms.tags]) {
      for (const t of list) {
        if (!checked[termKey(t)]) continue;
        if (t.uri) uris.add(t.uri);
        else if (t.label && !kws.has(t.label.toLowerCase())) {
          kws.add(t.label.toLowerCase());
          d.keywords = d.keywords ? `${d.keywords}, ${t.label}` : t.label;
        }
      }
    }
    d.uris = [...uris].join("\n");
    drafts = [...drafts];
    checked = {};
    preview = null;
  }

  async function runPreview(): Promise<void> {
    busy = "preview";
    error = "";
    try {
      preview = await previewDiversityCrosswalk({ categories: categories() }, split(scope, /\s+/));
    } catch (e) {
      error = humanApiMessage(e, "preview failed");
    } finally {
      busy = "";
    }
  }

  async function save(body: { categories?: CrosswalkCategory[]; toml?: string }): Promise<void> {
    busy = "save";
    error = "";
    notice = "";
    try {
      applyView(await saveDiversityCrosswalk(body));
      notice = "Saved. The Diversity screen now audits these categories.";
      preview = null;
    } catch (e) {
      error = humanApiMessage(e, "saving failed");
    } finally {
      busy = "";
    }
  }

  async function reset(): Promise<void> {
    busy = "reset";
    error = "";
    notice = "";
    try {
      await deleteDiversityCrosswalk();
      applyView(await fetchDiversityCrosswalk());
      notice = "Override removed; the audit runs the built-in seed.";
      preview = null;
    } catch (e) {
      error = humanApiMessage(e, "removing the override failed");
    } finally {
      busy = "";
    }
  }

  function matches(t: AuditTerm): boolean {
    if (!search) return true;
    const q = search.toLowerCase();
    return (t.label ?? "").toLowerCase().includes(q) || (t.uri ?? "").toLowerCase().includes(q) || (t.scheme ?? "").includes(q);
  }

  const sections = $derived(
    terms
      ? [
          { name: "Controlled subjects", items: terms.uris.filter(matches), total: terms.uriTotal },
          { name: "Heading labels", items: terms.headings.filter(matches), total: terms.headingTotal },
          { name: "Tags", items: terms.tags.filter(matches), total: terms.tagTotal },
        ]
      : [],
  );
</script>

<main class="divconfig" id="main" tabindex="-1">
  <header>
    <h1>Diversity crosswalk</h1>
    <p class="muted">
      Configure the categories the <a href="#/diversity">diversity audit</a> reports. Your changes extend the built-in
      seed: a category with a seed id gains your keywords and URIs, a new id becomes a new category. Seed matching
      stays active underneath. Categories overlap by design -- they are lenses, not a partition.
    </p>
  </header>

  {#if error}
    <p class="error" role="alert">{error}</p>
  {/if}
  {#if notice}
    <p class="notice" role="status">{notice}</p>
  {/if}

  {#if loading}
    <p class="muted">Loading…</p>
  {:else if view}
    {#if view.broken}
      <p class="error" role="alert">
        The stored override no longer parses ({view.broken}); the audit is running the built-in seed. Fix it in the
        TOML editor below or remove it.
      </p>
    {/if}

    <section class="editor">
      <h2>Your categories</h2>
      {#if drafts.length === 0}
        <p class="muted">
          No override yet. Add a category, or extend a seed category below -- then pick its terms from the histogram.
        </p>
      {/if}
      {#each drafts as d, i (i)}
        <fieldset class="cat" class:target={i === target}>
          <legend>
            <input class="id" placeholder="id (e.g. lgbtqia-trans)" bind:value={d.id} aria-label="category id" />
            <input class="label" placeholder="Label" bind:value={d.label} aria-label="category label" />
            <button type="button" class="pick" onclick={() => (target = i)} disabled={i === target}>
              {i === target ? "receiving terms" : "send terms here"}
            </button>
            <button type="button" class="remove" onclick={() => removeCategory(i)}>Remove</button>
          </legend>
          <label>
            Keywords <span class="hint">comma-separated; match heading labels whole-word</span>
            <input bind:value={d.keywords} placeholder="transgender, trans men" />
          </label>
          <label>
            Subject URIs <span class="hint">one per line; exact matches</span>
            <textarea rows="2" bind:value={d.uris}></textarea>
          </label>
          <label>
            Schemes <span class="hint">comma-separated vocabulary codes; every term of the scheme matches</span>
            <input bind:value={d.schemes} placeholder="homosaurus" />
          </label>
        </fieldset>
      {/each}
      <div class="actions">
        <button type="button" onclick={addCategory}>Add category</button>
        <button type="button" onclick={runPreview} disabled={drafts.length === 0 || busy !== ""}>
          {busy === "preview" ? "Previewing…" : "Preview counts"}
        </button>
        <button type="button" class="primary" onclick={() => save({ categories: categories() })} disabled={busy !== ""}>
          {busy === "save" ? "Saving…" : "Save override"}
        </button>
        {#if view.toml || drafts.length > 0}
          <button type="button" class="danger" onclick={reset} disabled={busy !== ""}>Remove override</button>
        {/if}
      </div>

      {#if preview}
        <table class="preview">
          <caption>
            Preview -- {preview.totalWorks} works{preview.scope ? ` (${preview.scope})` : ""}, nothing saved yet
          </caption>
          <thead><tr><th scope="col">Category</th><th scope="col" class="n">Works</th></tr></thead>
          <tbody>
            {#each preview.categories as c (c.id)}
              <tr><td>{c.label || c.id}</td><td class="n">{c.works}</td></tr>
            {/each}
          </tbody>
        </table>
      {/if}
    </section>

    <section class="builder">
      <h2>Pick from your collection</h2>
      <p class="muted">
        Every subject term in scope with its work count -- check the ones that belong together, then send them to a
        category. URIs land as exact matches; headings and tags land as keywords.
      </p>
      <form
        class="scope"
        onsubmit={(e) => {
          e.preventDefault();
          void loadAll();
        }}
      >
        <label for="dc-scope">Scope</label>
        <input id="dc-scope" bind:value={scope} placeholder="key=value key2=value2" />
        <label for="dc-search">Find</label>
        <input id="dc-search" type="search" bind:value={search} placeholder="filter terms" />
        <button type="submit">Apply scope</button>
      </form>
      {#if selectedCount > 0}
        <div class="send">
          <button type="button" class="primary" onclick={addSelected} disabled={drafts.length === 0}>
            Add {selectedCount} selected to {drafts[target]?.label || drafts[target]?.id || "the category"}
          </button>
          {#if drafts.length === 0}<span class="hint">add a category first</span>{/if}
        </div>
      {/if}
      {#each sections as sec (sec.name)}
        {#if sec.items.length > 0}
          <h3>{sec.name} <span class="hint">{sec.items.length} shown of {sec.total}</span></h3>
          <ul class="terms">
            {#each sec.items as t (termKey(t))}
              <li>
                <label>
                  <input type="checkbox" bind:checked={checked[termKey(t)]} />
                  <span class="term-label">{t.label || t.uri}</span>
                  {#if t.scheme}<span class="scheme">{t.scheme}</span>{/if}
                  {#if t.uri && t.label}<span class="uri" title={t.uri}>{t.uri}</span>{/if}
                  <span class="count">{t.works}</span>
                </label>
              </li>
            {/each}
          </ul>
        {/if}
      {/each}
    </section>

    <section class="seed">
      <h2>Built-in seed</h2>
      <ul class="seedlist">
        {#each view.seed as c (c.id)}
          <li>
            <strong>{c.label || c.id}</strong>
            <span class="hint">
              {(c.keywords ?? []).length} keywords, {(c.uris ?? []).length} URIs, {(c.schemes ?? []).length} schemes
            </span>
            <button type="button" onclick={() => extendSeed(c.id, c.label ?? "")}>Extend</button>
          </li>
        {/each}
      </ul>
    </section>

    <details class="advanced">
      <summary>Advanced: edit as TOML</summary>
      <p class="muted">
        The override persists as the same TOML <code>lcat diversity-audit --crosswalk</code> reads -- paste a file you
        already maintain, or copy this one out for the CLI.
      </p>
      <textarea rows="12" bind:value={tomlDraft} aria-label="override TOML"></textarea>
      <button type="button" onclick={() => save({ toml: tomlDraft })} disabled={busy !== "" || !tomlDraft.trim()}>
        Save TOML
      </button>
    </details>
  {/if}
</main>

<style>
  .divconfig {
    max-width: 60rem;
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
  h3 {
    margin: 0.9rem 0 0.3rem;
    font-size: 0.95rem;
  }
  .muted {
    color: var(--ink-muted, #667);
  }
  .hint {
    color: var(--ink-muted, #667);
    font-size: 0.78rem;
    font-weight: 400;
  }
  .error {
    color: var(--danger, #b00020);
  }
  .notice {
    color: var(--ok-ink, #1a6b32);
  }
  .cat {
    border: 1px solid var(--rule, #dde);
    border-radius: 6px;
    padding: 0.6rem 0.8rem;
    margin: 0 0 0.75rem;
    display: grid;
    gap: 0.5rem;
  }
  .cat.target {
    border-color: var(--accent, #4a7dff);
  }
  .cat legend {
    display: flex;
    gap: 0.5rem;
    align-items: center;
    flex-wrap: wrap;
  }
  .cat label {
    display: grid;
    gap: 0.2rem;
    font-size: 0.85rem;
    font-weight: 500;
  }
  .cat input,
  .cat textarea {
    font: inherit;
    font-weight: 400;
  }
  .id {
    width: 14rem;
  }
  .label {
    width: 16rem;
  }
  .actions,
  .send {
    display: flex;
    gap: 0.5rem;
    align-items: center;
    flex-wrap: wrap;
    margin-top: 0.5rem;
  }
  .preview {
    margin-top: 0.75rem;
    border-collapse: collapse;
  }
  .preview caption {
    text-align: left;
    font-size: 0.85rem;
    color: var(--ink-muted, #667);
    margin-bottom: 0.3rem;
  }
  .preview td,
  .preview th {
    border-bottom: 1px solid var(--rule, #dde);
    padding: 0.25rem 0.75rem 0.25rem 0;
    text-align: left;
  }
  .preview .n {
    text-align: right;
  }
  .scope {
    display: flex;
    gap: 0.5rem;
    align-items: center;
    flex-wrap: wrap;
    margin: 0.5rem 0;
  }
  .terms {
    list-style: none;
    margin: 0;
    padding: 0;
    max-height: 22rem;
    overflow-y: auto;
    border: 1px solid var(--rule, #dde);
    border-radius: 6px;
  }
  .terms li {
    border-bottom: 1px solid var(--rule, #eef);
  }
  .terms label {
    display: flex;
    gap: 0.5rem;
    align-items: center;
    padding: 0.25rem 0.6rem;
    font-size: 0.88rem;
  }
  .term-label {
    min-width: 0;
  }
  .scheme {
    font-size: 0.7rem;
    text-transform: uppercase;
    letter-spacing: 0.03em;
    color: var(--ink-muted, #667);
    border: 1px solid var(--rule, #dde);
    border-radius: 3px;
    padding: 0 0.3rem;
  }
  .uri {
    font-size: 0.72rem;
    color: var(--ink-muted, #667);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    max-width: 18rem;
  }
  .count {
    margin-left: auto;
    font-variant-numeric: tabular-nums;
    font-weight: 600;
  }
  .seedlist {
    list-style: none;
    margin: 0;
    padding: 0;
  }
  .seedlist li {
    display: flex;
    gap: 0.6rem;
    align-items: baseline;
    padding: 0.2rem 0;
  }
  .advanced textarea {
    width: 100%;
    font-family: var(--mono, ui-monospace, monospace);
    font-size: 0.8rem;
  }
</style>
