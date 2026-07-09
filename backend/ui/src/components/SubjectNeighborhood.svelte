<script lang="ts">
  // The crosswalking panel under an expanded subject chip (tasks/071): the
  // term's also-known-as labels and definition, then its SKOS neighborhood
  // -- broader, narrower, related, and siblings (the broader terms' other
  // children) -- each neighbor with Replace and Add actions that stage
  // ordinary ops through the parent form.
  import { onMount } from "svelte";
  import { resolveTerm, resolveTermURIs } from "../lib/api";
  import { allAltLabels, bestDefinition, bestLabel } from "../lib/vocab";
  import type { Term } from "../lib/types";

  let {
    term,
    present,
    onreplace,
    onadd,
  }: {
    term: Term;
    /** IRIs the record's field already carries. A neighbor in this set cannot
     *  be added again -- the panel exists to crosswalk terms onto a record, and
     *  on a record where that was already done, Add would stage an edit that
     *  changes nothing (tasks/248). */
    present: Set<string>;
    onreplace: (t: Term) => void;
    onadd: (t: Term) => void;
  } = $props();

  interface Group {
    rel: string;
    terms: Term[];
  }

  let groups = $state<Group[]>([]);
  // Equivalents (tasks/072): skos:exactMatch/closeMatch links resolved
  // scheme-agnostically -- the one-click homosaurus->LCSH crosswalk.
  let equivalents = $state<Term[]>([]);
  let unresolvedEq = $state<string[]>([]);
  let loading = $state(true);

  onMount(() => void load());

  async function resolveAll(ids: string[]): Promise<Term[]> {
    const settled = await Promise.allSettled(ids.map((id) => resolveTerm(term.scheme, id)));
    return settled.filter((r) => r.status === "fulfilled").map((r) => r.value);
  }

  async function load(): Promise<void> {
    const [broader, narrower, related] = await Promise.all([
      resolveAll(term.broader ?? []),
      resolveAll(term.narrower ?? []),
      resolveAll(term.related ?? []),
    ]);
    // Siblings: the broader terms' other narrower children.
    const seen = new Set([term.id, ...(term.narrower ?? []), ...(term.related ?? [])]);
    const siblingIds: string[] = [];
    for (const parent of broader) {
      for (const id of parent.narrower ?? []) {
        if (!seen.has(id)) {
          seen.add(id);
          siblingIds.push(id);
        }
      }
    }
    const siblings = await resolveAll(siblingIds);
    // Equivalents cross schemes, so they resolve scheme-agnostically.
    const eqIds = [...(term.exactMatch ?? []), ...(term.closeMatch ?? [])];
    if (eqIds.length > 0) {
      try {
        const res = await resolveTermURIs(eqIds);
        equivalents = eqIds.flatMap((id) => (res.terms[id] ? [res.terms[id]] : []));
        unresolvedEq = eqIds.filter((id) => !res.terms[id]);
      } catch {
        unresolvedEq = eqIds;
      }
    }
    groups = [
      { rel: "Broader", terms: broader },
      { rel: "Narrower", terms: narrower },
      { rel: "Related", terms: related },
      { rel: "Siblings", terms: siblings },
    ].filter((g) => g.terms.length > 0);
    loading = false;
  }
</script>

<!-- Replace stays offered for a term the record already has: it still removes
     the expanded subject, which is how a cataloger drops the source term once
     the crosswalk target is on the record. Add does not, because it would do
     nothing. -->
{#snippet actions(t: Term)}
  {@const has = present.has(t.id)}
  <button
    class="button act"
    onclick={() => onreplace(t)}
    title={has
      ? "Remove " + bestLabel(term) + " (this record already has " + bestLabel(t) + ")"
      : "Replace this subject with " + bestLabel(t)}
  >
    Replace
  </button>
  {#if has}
    <span class="have" title="this record already has this subject">already a subject</span>
  {:else}
    <button class="button button--quiet act" onclick={() => onadd(t)} title={"Also add " + bestLabel(t)}>
      Add
    </button>
  {/if}
{/snippet}

<div class="hood" aria-label={"Neighborhood of " + bestLabel(term)}>
  {#if allAltLabels(term).length > 0}
    <p class="aka"><span class="muted">Also known as:</span> {allAltLabels(term).join("; ")}</p>
  {/if}
  {#if bestDefinition(term)}
    <p class="def muted">{bestDefinition(term)}</p>
  {/if}

  {#if loading}
    <p class="muted small" role="status">Loading neighborhood…</p>
  {:else if groups.length === 0 && equivalents.length === 0 && unresolvedEq.length === 0}
    <p class="muted small">No broader, narrower, related, sibling, or equivalent terms.</p>
  {:else}
    {#if equivalents.length > 0 || unresolvedEq.length > 0}
      <div class="rel">
        <h4>Equivalents</h4>
        <ul>
          {#each equivalents as t (t.id)}
            <li>
              <span class="nlabel" title={bestDefinition(t) || t.id}>{bestLabel(t)}</span>
              <span class="eq-scheme">{t.scheme}</span>
              {@render actions(t)}
            </li>
          {/each}
          {#each unresolvedEq as id (id)}
            <li>
              <span class="nlabel uri muted" title="not in the local index -- install its vocabulary to crosswalk">{id}</span>
            </li>
          {/each}
        </ul>
      </div>
    {/if}
    {#each groups as g (g.rel)}
      <div class="rel">
        <h4>{g.rel}</h4>
        <ul>
          {#each g.terms as t (t.id)}
            <li>
              <span class="nlabel" title={bestDefinition(t) || t.id}>{bestLabel(t)}</span>
              {@render actions(t)}
            </li>
          {/each}
        </ul>
      </div>
    {/each}
  {/if}
</div>

<style>
  .hood {
    border: 1px solid var(--rule);
    border-left: 3px solid var(--accent);
    border-radius: 6px;
    background: var(--surface);
    padding: 0.45rem 0.8rem 0.6rem;
    margin: 0.2rem 0 0.4rem;
    max-width: 34rem;
  }
  .aka,
  .def {
    font-size: 0.85rem;
    margin: 0.2rem 0;
  }
  .small {
    font-size: 0.8rem;
  }
  .rel h4 {
    margin: 0.45rem 0 0.1rem;
    font-size: 0.72rem;
    text-transform: uppercase;
    letter-spacing: 0.06em;
    color: var(--ink-muted);
  }
  .rel ul {
    list-style: none;
    margin: 0;
    padding: 0;
  }
  .rel li {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    padding: 0.12rem 0;
  }
  .nlabel {
    flex: 1;
    min-width: 8rem;
  }
  .nlabel.uri {
    font-family: var(--mono);
    font-size: 0.75rem;
    word-break: break-all;
  }
  /* Occupies the Add button's slot, so the row does not reflow between a
     term the record has and one it does not. */
  .have {
    font-size: 0.72rem;
    font-style: italic;
    color: var(--ink-muted);
    white-space: nowrap;
    padding: 0.05em 0.55em;
  }

  .eq-scheme {
    font-size: 0.66rem;
    font-weight: 650;
    text-transform: uppercase;
    letter-spacing: 0.05em;
    color: var(--ink-muted);
    border: 1px solid var(--rule);
    border-radius: 999px;
    padding: 0.05em 0.55em;
  }
  .act {
    font-size: 0.72rem;
    padding: 0.05em 0.65em;
  }
</style>
