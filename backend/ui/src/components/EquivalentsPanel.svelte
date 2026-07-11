<script lang="ts">
  // Cross-vocabulary equivalents for the work's controlled subjects: the
  // cataloging move this exists for is "the record has the FAST topic; add
  // the Homosaurus synonym". Suggestions come from skos exact/close matches
  // in both directions plus one-hop pivots, and every row says its match
  // strength -- a pivot is only as good as its weakest hop, so the panel
  // never dresses one up as a synonym. Adding stages the same subjects op
  // the lookup uses; nothing is ever added automatically.
  import { fetchTermEquivalents, resolveTermURIs, humanApiMessage } from "../lib/api";
  import { isReadOnly } from "../lib/config";
  import { presentIRIs } from "../lib/subjects";
  import type { FieldValue, Op, Term, TermEquivalent } from "../lib/types";

  let {
    subjects = [],
    ops = [],
    onadd,
  }: {
    subjects?: FieldValue[];
    ops?: Op[];
    onadd: (uri: string) => void;
  } = $props();

  const readOnly = isReadOnly();

  // sourceLimit bounds how many subjects fan out into equivalents lookups; a
  // record with dozens of headings gets the first N and says so.
  const sourceLimit = 12;

  type Suggestion = TermEquivalent & { source: string };

  let groups = $state<{ source: string; label: string; suggestions: Suggestion[] }[]>([]);
  let truncated = $state(0);
  let loading = $state(false);
  let error = $state("");

  /** Present = stored IRIs minus staged removals, plus staged adds: a term
   *  just added must neither re-suggest nor re-add. */
  function presentSet(): Set<string> {
    const present = presentIRIs(subjects, ops);
    for (const op of ops) {
      if (op.action === "add" && op.value?.iri && op.value.v) present.add(op.value.v);
    }
    return present;
  }

  function strengthRank(s: string): number {
    return { exact: 4, close: 3, "pivot-exact": 2, "pivot-close": 1 }[s] ?? 0;
  }

  function label(e: { labels?: Record<string, string>; id: string }): string {
    const l = e.labels ?? {};
    return l.en || l[""] || Object.values(l)[0] || e.id;
  }

  // The refetch key: the present set's sorted join. $derived so a staged
  // add/remove re-runs the effect below without polling.
  const presentKey = $derived([...presentSet()].sort().join("\n"));

  $effect(() => {
    void presentKey;
    void refresh();
  });

  async function refresh(): Promise<void> {
    const present = presentSet();
    const sources = [...present].slice(0, sourceLimit);
    truncated = Math.max(0, present.size - sources.length);
    if (sources.length === 0) {
      groups = [];
      return;
    }
    loading = true;
    error = "";
    try {
      const results = await Promise.all(
        sources.map((uri) =>
          fetchTermEquivalents(uri).then(
            (r) => ({ uri, equivalents: r.equivalents ?? [] }),
            () => ({ uri, equivalents: [] as TermEquivalent[] }), // an unknown source term suggests nothing
          ),
        ),
      );
      // Dedupe across sources by target URI, keeping the strongest claim.
      const best = new Map<string, Suggestion>();
      for (const { uri, equivalents } of results) {
        for (const e of equivalents) {
          if (present.has(e.id)) continue;
          const have = best.get(e.id);
          if (!have || strengthRank(e.strength) > strengthRank(have.strength)) {
            best.set(e.id, { ...e, source: uri });
          }
        }
      }
      const bySource = new Map<string, Suggestion[]>();
      for (const s of best.values()) {
        bySource.set(s.source, [...(bySource.get(s.source) ?? []), s]);
      }
      const sourceLabels: Record<string, string> = {};
      if (bySource.size) {
        const { terms } = await resolveTermURIs([...bySource.keys()]);
        for (const [uri, t] of Object.entries((terms ?? {}) as Record<string, Term>)) {
          sourceLabels[uri] = label({ labels: t.labels, id: uri });
        }
      }
      groups = [...bySource.entries()]
        .map(([source, suggestions]) => ({
          source,
          label: sourceLabels[source] ?? source,
          suggestions: suggestions.sort(
            (a, b) => strengthRank(b.strength) - strengthRank(a.strength) || label(a).localeCompare(label(b)),
          ),
        }))
        .sort((a, b) => a.label.localeCompare(b.label));
    } catch (e) {
      error = humanApiMessage(e, "loading equivalents failed");
    } finally {
      loading = false;
    }
  }

  const count = $derived(groups.reduce((n, g) => n + g.suggestions.length, 0));
</script>

{#if count > 0 || loading || error}
  <details class="equivalents">
    <summary>
      Cross-vocabulary equivalents
      {#if count > 0}<span class="count">{count}</span>{/if}
    </summary>
    {#if error}
      <p class="error" role="alert">{error}</p>
    {:else if loading}
      <p class="muted">Looking up…</p>
    {:else}
      <p class="note muted">
        Terms other vocabularies state as matching this record's subjects.
        Strength names the weakest link; a pivot goes through a shared third
        term. Nothing is added without you.
      </p>
      {#each groups as g (g.source)}
        <div class="group">
          <h4>≈ {g.label}</h4>
          <ul>
            {#each g.suggestions as s (s.id)}
              <li>
                <span class="term-label">{label(s)}</span>
                {#if s.scheme}<span class="scheme">{s.scheme}</span>{/if}
                <span class={`strength s-${s.strength}`} title={s.via ? `via ${s.via}` : undefined}>{s.strength}</span>
                {#if s.known && !readOnly}
                  <button type="button" class="add" onclick={() => onadd(s.id)}>Add</button>
                {:else if !s.known}
                  <span class="muted unknown" title={s.id}>not in a loaded vocabulary</span>
                {/if}
              </li>
            {/each}
          </ul>
        </div>
      {/each}
      {#if truncated > 0}
        <p class="muted note">…and {truncated} more subject{truncated === 1 ? "" : "s"} not searched (first {sourceLimit} only).</p>
      {/if}
    {/if}
  </details>
{/if}

<style>
  .equivalents {
    margin-top: 0.75rem;
  }
  summary {
    cursor: pointer;
    font-weight: 500;
  }
  .count {
    display: inline-block;
    min-width: 1.3em;
    text-align: center;
    background: var(--accent, #4a7dff);
    color: var(--accent-ink, #fff);
    border-radius: 999px;
    font-size: 0.75rem;
    padding: 0.05rem 0.35rem;
    margin-left: 0.35rem;
  }
  .note {
    font-size: var(--fs-meta, 0.8rem);
    margin: 0.4rem 0;
  }
  .group h4 {
    margin: 0.6rem 0 0.15rem;
    font-size: 0.85rem;
    font-weight: 600;
  }
  ul {
    list-style: none;
    margin: 0;
    padding: 0;
  }
  li {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    padding: 0.2rem 0;
    font-size: 0.9rem;
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
  .strength {
    font-size: 0.7rem;
    border-radius: 3px;
    padding: 0 0.3rem;
  }
  .s-exact {
    background: var(--tint-ok, #e2f4e6);
  }
  .s-close {
    background: var(--surface-alt, #f2f3f5);
  }
  .s-pivot-exact,
  .s-pivot-close {
    background: var(--surface-alt, #f2f3f5);
    border: 1px dashed var(--rule, #ccd);
  }
  .add {
    margin-left: auto;
  }
  .unknown {
    margin-left: auto;
    font-size: 0.75rem;
  }
  .muted {
    color: var(--ink-muted, #667);
  }
  .error {
    color: var(--danger, #b00020);
  }
</style>
