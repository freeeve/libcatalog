<script lang="ts">
  // Read-only rendering of a WorkDoc: one section per profile field with a
  // provenance badge per value, overridden values struck through, instances
  // below, and the unclaimed passthrough quads in a collapsed details block.
  import type { ResourceDoc, WorkDoc } from "../lib/types";
  import ProvenanceBadge from "./ProvenanceBadge.svelte";

  let { doc, etag }: { doc: WorkDoc; etag: string } = $props();

  /** "subjectLabels" -> "Subject labels". */
  function prettify(path: string): string {
    const words = path.replace(/([a-z0-9])([A-Z])/g, "$1 $2").toLowerCase();
    return words.charAt(0).toUpperCase() + words.slice(1);
  }

  function sortedFields(res: ResourceDoc): [string, ResourceDoc["fields"][string]][] {
    return Object.entries(res.fields).sort(([a], [b]) => a.localeCompare(b));
  }
</script>

<article aria-label="Work {doc.workId}">
  <header class="dochead">
    <h1>{doc.workId}</h1>
    <p class="muted">profile {doc.profileId} · etag <code>{etag}</code></p>
  </header>

  <section aria-label="Work fields">
    {#each sortedFields(doc.work) as [path, values] (path)}
      <div class="field">
        <h2>{prettify(path)}</h2>
        <ul>
          {#each values as fv, i (fv.node + i)}
            <li class="value" class:overridden={fv.overridden}>
              <span class="v" class:iri={fv.iri}>{fv.v}</span>
              {#if fv.lang}<span class="lang">@{fv.lang}</span>{/if}
              <ProvenanceBadge prov={fv.prov} />
              {#if fv.overridden}<span class="ov-note">overridden</span>{/if}
            </li>
          {/each}
        </ul>
      </div>
    {:else}
      <p class="muted">No profile fields on this work.</p>
    {/each}
  </section>

  {#if doc.instances.length > 0}
    <section aria-label="Instances">
      <h2 class="instances-head">Instances</h2>
      {#each doc.instances as inst (inst.id)}
        <div class="instance">
          <h3>{inst.id}</h3>
          {#each sortedFields(inst) as [path, values] (path)}
            <div class="field">
              <h4>{prettify(path)}</h4>
              <ul>
                {#each values as fv, i (fv.node + i)}
                  <li class="value" class:overridden={fv.overridden}>
                    <span class="v" class:iri={fv.iri}>{fv.v}</span>
                    {#if fv.lang}<span class="lang">@{fv.lang}</span>{/if}
                    <ProvenanceBadge prov={fv.prov} />
                    {#if fv.overridden}<span class="ov-note">overridden</span>{/if}
                  </li>
                {/each}
              </ul>
            </div>
          {:else}
            <p class="muted">No profile fields.</p>
          {/each}
        </div>
      {/each}
    </section>
  {/if}

  {#if doc.passthrough.length > 0}
    <details class="passthrough">
      <summary>Passthrough ({doc.passthrough.length} statements)</summary>
      <pre>{doc.passthrough.join("\n")}</pre>
    </details>
  {/if}
</article>

<style>
  .dochead h1 {
    margin-bottom: 0.1rem;
    font-family: var(--mono);
    font-size: 1.15rem;
  }
  .dochead p {
    margin-top: 0;
    font-size: 0.85rem;
  }
  .field {
    margin: 0.9rem 0;
  }
  .field h2,
  .field h4 {
    margin: 0 0 0.25rem;
    font-size: 0.85rem;
    text-transform: uppercase;
    letter-spacing: 0.06em;
    color: var(--ink-muted);
  }
  .field ul {
    margin: 0;
    padding: 0;
    list-style: none;
  }
  .value {
    display: flex;
    align-items: baseline;
    gap: 0.5rem;
    padding: 0.15rem 0;
    flex-wrap: wrap;
  }
  .value .v.iri {
    font-family: var(--mono);
    font-size: 0.9em;
  }
  .value.overridden .v {
    text-decoration: line-through;
    color: var(--ink-muted);
  }
  .ov-note {
    font-size: 0.72rem;
    color: var(--danger);
    font-weight: 600;
  }
  .lang {
    color: var(--ink-muted);
    font-size: 0.8em;
  }
  .instances-head {
    border-top: 1px solid var(--rule);
    padding-top: 1rem;
    margin-top: 1.5rem;
  }
  .instance {
    border: 1px solid var(--rule);
    border-radius: 6px;
    padding: 0.4rem 1rem 0.7rem;
    margin: 0.75rem 0;
  }
  .instance h3 {
    font-family: var(--mono);
    font-size: 0.95rem;
    margin: 0.5rem 0 0.25rem;
  }
  .passthrough {
    margin-top: 1.5rem;
  }
  .passthrough summary {
    cursor: pointer;
    color: var(--ink-muted);
  }
  .passthrough pre {
    font-family: var(--mono);
    font-size: 0.78rem;
    background: var(--surface);
    border: 1px solid var(--rule);
    border-radius: 6px;
    padding: 0.75rem;
    overflow-x: auto;
  }
</style>
