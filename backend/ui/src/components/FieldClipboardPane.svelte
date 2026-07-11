<script lang="ts">
  // The field clipboard as a visible pane: cut/copied fields
  // ( ops, either surface) listed newest-first as their text lines;
  // each pastes back into the hosting editor through onpaste, so the same
  // pane serves grid and text mode.
  import { clipAt, clipClear, clipRemove, fieldClipboard } from "../lib/fieldClipboard.svelte";
  import { serializeField } from "../lib/mrk";
  import type { MarcField } from "../lib/types";

  let { onpaste }: { onpaste: (f: MarcField) => void } = $props();

  function paste(i: number): void {
    const f = clipAt(i);
    if (f) onpaste(f);
  }
</script>

<details class="pane" open={fieldClipboard.entries.length > 0}>
  <summary>
    Field clipboard ({fieldClipboard.entries.length})
    <span class="muted">Alt+C copies · Alt+X cuts · Alt+V pastes the newest</span>
  </summary>
  {#if fieldClipboard.entries.length === 0}
    <p class="muted empty">Nothing cut or copied yet.</p>
  {:else}
    <ul>
      {#each fieldClipboard.entries as f, i (i)}
        <li>
          <code>{serializeField(f)}</code>
          <span class="acts">
            <button class="button button--quiet mini" onclick={() => paste(i)}>Paste</button>
            <button class="button button--quiet mini" onclick={() => clipRemove(i)}>Remove</button>
          </span>
        </li>
      {/each}
    </ul>
    <button class="button button--quiet mini" onclick={clipClear}>Clear clipboard</button>
  {/if}
</details>

<style>
  .pane {
    border: 1px solid var(--rule);
    border-radius: 8px;
    padding: 0.4rem 0.7rem;
    margin: 0.5rem 0;
    font-size: 0.82rem;
  }
  summary {
    cursor: pointer;
    color: var(--ink-muted);
    font-weight: 600;
  }
  summary .muted {
    font-weight: 400;
    font-size: 0.75rem;
    margin-left: 0.5em;
  }
  .empty {
    margin: 0.4rem 0 0.2rem;
  }
  ul {
    list-style: none;
    margin: 0.4rem 0;
    padding: 0;
  }
  li {
    display: flex;
    gap: 0.6rem;
    align-items: baseline;
    justify-content: space-between;
    border-bottom: 1px solid var(--rule);
    padding: 0.15rem 0;
  }
  li:last-child {
    border-bottom: 0;
  }
  code {
    font-family: var(--mono);
    font-size: 0.78rem;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .acts {
    display: inline-flex;
    gap: 0.3rem;
    flex: none;
  }
  .mini {
    font-size: 0.72rem;
    padding: 0.05em 0.6em;
  }
</style>
