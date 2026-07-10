<script lang="ts">
  // The semantic neighborhood of one term: broader/narrower/related URIs
  // resolved to labels through the cached /v1/term lookup. Clicking a
  // neighbor walks the panel to that term; the breadcrumb walks back, and a
  // walked-to term can be picked directly via onselect. The parent keys this
  // component by term id, so the trail resets when the highlight moves.
  import { tick } from "svelte";
  import { resolveTerm } from "../lib/api";
  import { bestDefinition, bestLabel } from "../lib/vocab";
  import type { Term } from "../lib/types";

  let { term, onselect }: { term: Term; onselect?: (t: Term) => void } = $props();

  let trail = $state<Term[]>([]);
  let hood = $state<HTMLElement | null>(null);
  const current = $derived(trail[trail.length - 1] ?? term);
  const groups = $derived(
    [
      { rel: "Broader", ids: current.broader ?? [] },
      { rel: "Narrower", ids: current.narrower ?? [] },
      { rel: "Related", ids: current.related ?? [] },
    ].filter((g) => g.ids.length > 0),
  );

  // Clicking a neighbor unmounts the button that held focus, so the browser
  // drops focus to <body>. Move it deliberately to the new breadcrumb (or the
  // panel when we return to the root) so focus never leaves the dialog and a
  // screen reader announces the term just walked to (tasks/250). The Modal's
  // window-level trap is the safety net; this is the polite version.
  async function refocus(): Promise<void> {
    await tick();
    (hood?.querySelector<HTMLElement>(".here") ?? hood)?.focus();
  }

  function walk(t: Term): void {
    trail = [...trail, t];
    void refocus();
  }

  function back(): void {
    trail = trail.slice(0, -1);
    void refocus();
  }
</script>

<div class="hood" bind:this={hood} tabindex="-1">
  {#if trail.length > 0}
    <nav class="crumb" aria-label="Neighborhood trail">
      <button class="linkish" onclick={back}>← {bestLabel(trail[trail.length - 2] ?? term)}</button>
      <span class="here" tabindex="-1">{bestLabel(current)}</span>
      {#if onselect}
        <button class="button use" onclick={() => onselect?.(current)}>Use this term</button>
      {/if}
    </nav>
    {#if bestDefinition(current)}
      <p class="def muted">{bestDefinition(current)}</p>
    {/if}
  {/if}

  {#if groups.length === 0}
    <p class="muted none">No broader, narrower, or related terms.</p>
  {:else}
    {#each groups as g (g.rel)}
      <div class="rel">
        <h4>{g.rel}</h4>
        <ul>
          {#each g.ids as id (id)}
            <li>
              {#await resolveTerm(current.scheme, id)}
                <span class="muted uri">{id}</span>
              {:then t}
                <button class="linkish" onclick={() => walk(t)}>{bestLabel(t)}</button>
              {:catch}
                <span class="muted uri" title="not in the loaded vocabulary">{id}</span>
              {/await}
            </li>
          {/each}
        </ul>
      </div>
    {/each}
  {/if}
</div>

<style>
  .hood {
    border-top: 1px solid var(--rule);
    margin-top: 0.6rem;
    padding-top: 0.4rem;
  }
  .crumb {
    display: flex;
    align-items: baseline;
    gap: 0.6rem;
    flex-wrap: wrap;
    margin: 0.3rem 0;
  }
  .here {
    font-weight: 600;
  }
  .use {
    font-size: 0.8rem;
    padding: 0.2em 0.8em;
  }
  .rel h4 {
    margin: 0.5rem 0 0.15rem;
    font-size: 0.78rem;
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
    padding: 0.1rem 0;
  }
  .linkish {
    background: none;
    border: 0;
    padding: 0;
    color: var(--accent);
    text-decoration: underline;
    text-align: left;
  }
  .uri {
    font-family: var(--mono);
    font-size: 0.75rem;
    word-break: break-all;
  }
  .def,
  .none {
    font-size: 0.85rem;
  }
</style>
