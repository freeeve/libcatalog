<script lang="ts">
  // The semantic neighborhood of one term: broader/narrower/related URIs
  // resolved to labels through the cached /v1/term lookup. Clicking a
  // neighbor walks the panel to that term; the breadcrumb walks back, and a
  // walked-to term can be picked directly via onselect. The parent keys this
  // component by term id, so the trail resets when the highlight moves.
  import { tick } from "svelte";
  import { resolveTerm } from "../lib/api";
  import { allAltLabels, bestDefinition, bestLabel } from "../lib/vocab";
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
  // drops focus to <body>. Move it deliberately to the identity heading of the
  // term just walked to (or the panel as a fallback) so focus never leaves the
  // dialog and a screen reader announces the new term. The Modal's
  // window-level trap is the safety net; this is the polite version.
  async function refocus(): Promise<void> {
    await tick();
    (hood?.querySelector<HTMLElement>(".ident") ?? hood)?.focus();
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
  <!-- The identity block lives here, driven by this panel's own current, so
       the heading, URI, definition and variants always name the same term the
       "Use this term" button stages. -->
  <h3 class="ident" tabindex="-1">
    {#if current.path?.length}<span class="path">{current.path.map((p) => p.label).join(" › ") + " › "}</span>{/if}{bestLabel(current)}
  </h3>
  <p class="uri ident-uri">{current.id}</p>
  {#if bestDefinition(current)}
    <p class="def">{bestDefinition(current)}</p>
  {/if}
  {#if allAltLabels(current).length > 0}
    <p class="alt"><span class="muted">Also known as:</span> {allAltLabels(current).join("; ")}</p>
  {/if}

  {#if trail.length > 0}
    <nav class="crumb" aria-label="Neighborhood trail">
      <button class="linkish" onclick={back}>← Back to {bestLabel(trail[trail.length - 2] ?? term)}</button>
      {#if onselect}
        <button class="button use" onclick={() => onselect?.(current)}>Use this term</button>
      {/if}
    </nav>
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
  .hood:focus,
  .ident:focus {
    outline: none;
  }
  .ident {
    margin: 0.2rem 0 0.1rem;
    font-size: 1rem;
  }
  .path {
    font-weight: 400;
    color: var(--ink-muted);
  }
  .ident-uri {
    color: var(--ink-muted);
    margin: 0.1rem 0;
  }
  .alt {
    font-size: 0.85rem;
  }
  .crumb {
    display: flex;
    align-items: baseline;
    gap: 0.6rem;
    flex-wrap: wrap;
    margin: 0.3rem 0 0.5rem;
    padding-top: 0.4rem;
    border-top: 1px solid var(--rule);
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
