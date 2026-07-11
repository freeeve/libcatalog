<script lang="ts">
  // Work-to-work relationships: lists the work's
  // editorial hasPart/partOf links with titles, adds a link by target work
  // id, removes one. Writes are immediate (like items and covers), not
  // staged ops; the inverse statement lands on the target work.
  import { onMount } from "svelte";
  import { addRelation, fetchRelations, humanApiMessage, removeRelation, type RelationEntry } from "../lib/api";
  import { isReadOnly } from "../lib/config";

  let { workId }: { workId: string } = $props();

  const readOnly = isReadOnly();
  const KINDS = [
    { kind: "hasPart" as const, title: "Has part", empty: "no parts" },
    { kind: "partOf" as const, title: "Part of", empty: "not part of anything" },
  ];

  let rel = $state<{ hasPart: RelationEntry[]; partOf: RelationEntry[] }>({ hasPart: [], partOf: [] });
  let newKind = $state<"hasPart" | "partOf">("hasPart");
  let newTarget = $state("");
  let busy = $state(false);
  let error = $state("");

  async function load(): Promise<void> {
    try {
      // An empty relation list arrives as a Go nil slice, i.e. null; normalize
      // so the panel indexes them the way fetchWorkDoc normalizes its own.
      const got = await fetchRelations(workId);
      rel = { hasPart: got?.hasPart ?? [], partOf: got?.partOf ?? [] };
    } catch (e) {
      error = humanApiMessage(e, "loading relationships failed");
    }
  }

  async function link(): Promise<void> {
    const target = newTarget.trim();
    if (!target) return;
    busy = true;
    error = "";
    try {
      await addRelation(workId, newKind, target);
      newTarget = "";
      await load();
    } catch (e) {
      error = humanApiMessage(e, "linking failed");
    } finally {
      busy = false;
    }
  }

  async function unlink(kind: "hasPart" | "partOf", target: string): Promise<void> {
    busy = true;
    error = "";
    try {
      await removeRelation(workId, kind, target);
      await load();
    } catch (e) {
      error = humanApiMessage(e, "unlinking failed");
    } finally {
      busy = false;
    }
  }

  onMount(() => void load());
</script>

<section class="relpanel" aria-label="Relationships">
  <h3>Relationships</h3>
  {#each KINDS as k (k.kind)}
    <h4>{k.title}</h4>
    {#if rel[k.kind].length === 0}
      <p class="muted">{k.empty}</p>
    {:else}
      <ul>
        {#each rel[k.kind] as entry (entry.workId)}
          <li>
            <a href={"#/works/" + encodeURIComponent(entry.workId)}>{entry.title || entry.workId}</a>
            {#if entry.title}<code class="muted">{entry.workId}</code>{/if}
            {#if !readOnly}
              <button class="button button--quiet" onclick={() => void unlink(k.kind, entry.workId)} disabled={busy} title="remove this link (both directions)">×</button>
            {/if}
          </li>
        {/each}
      </ul>
    {/if}
  {/each}
  {#if !readOnly}
    <form
      class="addrow"
      onsubmit={(ev) => {
        ev.preventDefault();
        void link();
      }}
    >
      <select bind:value={newKind} aria-label="Relation kind">
        <option value="hasPart">has part</option>
        <option value="partOf">part of</option>
      </select>
      <input type="text" bind:value={newTarget} placeholder="target work id (w…)" aria-label="Target work id" />
      <button type="submit" class="button" disabled={busy || !newTarget.trim()}>Link</button>
    </form>
  {/if}
  {#if error}<p class="error" role="alert">{error}</p>{/if}
</section>

<style>
  .relpanel h3 {
    margin: 0.4rem 0 0.2rem;
  }
  .relpanel h4 {
    margin: 0.4rem 0 0.1rem;
    font-size: var(--fs-meta);
    text-transform: uppercase;
    letter-spacing: 0.04em;
    color: var(--ink-muted);
  }
  .relpanel ul {
    margin: 0.1rem 0;
    padding-left: 1.1rem;
  }
  .relpanel li {
    display: flex;
    align-items: baseline;
    gap: 0.5em;
  }
  .relpanel code {
    font-size: var(--fs-meta);
  }
  .addrow {
    display: flex;
    gap: 0.4rem;
    margin-top: 0.4rem;
    align-items: center;
  }
  .addrow input {
    width: 14rem;
    font: inherit;
  }
</style>
