<script lang="ts">
  // "More like this" (tasks/284): the work's computed neighbours, scored live
  // over the admin corpus by the same scorer the OPAC's build step runs. So this
  // panel shows the rail a reader will get after the next publish -- and it moves
  // as soon as the cataloger re-subjects the record, which is the whole reason it
  // is a live query rather than a read of the projected sidecar.
  //
  // These are computed, not asserted. RelationsPanel above carries hasPart/partOf,
  // which a cataloger stated on purpose; nothing here was stated by anyone, so the
  // panel says so and every row explains itself.
  import { onMount } from "svelte";
  import { fetchSimilar, humanApiMessage, resolveTermURIs, type SimilarNeighbor } from "../lib/api";
  import type { Term } from "../lib/types";

  let { workId }: { workId: string } = $props();

  let neighbours = $state<SimilarNeighbor[]>([]);
  let labels = $state<Record<string, string>>({});
  let loading = $state(true);
  let error = $state("");

  /** Shared values are opaque: subjects are authority IRIs, tags and contributors
   *  and series are already human text. Only the IRIs need resolving. */
  function isURI(v: string): boolean {
    return v.startsWith("http://") || v.startsWith("https://");
  }

  function labelFor(v: string): string {
    return labels[v] ?? v;
  }

  /** A term's display label: the UI is English-only, and an untagged label ("")
   *  is what a local vocabulary usually carries. Falls back to the URI. */
  function prefLabel(t: Term, uri: string): string {
    const l = t.labels ?? {};
    return l.en || l[""] || Object.values(l)[0] || uri;
  }

  async function load(): Promise<void> {
    loading = true;
    error = "";
    try {
      const got = await fetchSimilar(workId);
      neighbours = got?.similar ?? [];
      const uris = [...new Set(neighbours.flatMap((n) => n.shared ?? []).filter(isURI))];
      if (uris.length) {
        // A URI the vocabulary cannot resolve is absent from the map, and
        // labelFor falls back to the URI rather than rendering "undefined".
        const { terms } = await resolveTermURIs(uris);
        labels = Object.fromEntries(
          Object.entries(terms ?? {}).map(([uri, t]: [string, Term]) => [uri, prefLabel(t, uri)]),
        );
      }
    } catch (e) {
      error = humanApiMessage(e, "loading similar works failed");
    } finally {
      loading = false;
    }
  }

  onMount(load);
</script>

<div class="similar">
  <p class="note">
    Suggested automatically from shared subjects, tags, contributors and series. Nobody catalogued these
    links; the reader sees the same rail after the next publish.
  </p>

  {#if error}
    <p class="error" role="alert">{error}</p>
  {:else if loading}
    <p class="muted">Scoring…</p>
  {:else if neighbours.length === 0}
    <p class="muted">
      No neighbours. This record shares no subject, tag, contributor or series with any other work you hold
      &mdash; adding a controlled subject is the fastest way to change that.
    </p>
  {:else}
    <ul>
      {#each neighbours as n (n.workId)}
        <li>
          <a href="#/works/{n.workId}">{n.title || n.workId}</a>
          {#if n.shared?.length}
            <span class="why">shares {n.shared.map(labelFor).join(", ")}</span>
          {/if}
        </li>
      {/each}
    </ul>
  {/if}
</div>

<style>
  .similar ul {
    list-style: none;
    padding: 0;
    margin: 0;
  }
  .similar li {
    padding: 0.3rem 0;
  }
  .note,
  .muted {
    color: var(--ink-muted);
    font-size: var(--fs-meta);
  }
  .note {
    margin: 0 0 0.6rem;
  }
  .why {
    display: block;
    color: var(--ink-muted);
    font-size: var(--fs-meta);
  }
</style>
