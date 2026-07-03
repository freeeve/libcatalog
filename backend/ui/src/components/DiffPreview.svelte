<script lang="ts">
  // Dry-run diff renderer: the exact N-Quads delta a save would make, as
  // +/- monospace lines. Collapsible so long deltas don't bury the form.
  import type { Diff } from "../lib/types";

  let { diff, onclose }: { diff: Diff; onclose?: () => void } = $props();
</script>

<section class="diff" aria-label="Change preview">
  <details open>
    <summary>
      Preview: <strong>{diff.added.length}</strong> added, <strong>{diff.removed.length}</strong> removed
    </summary>
    <div class="lines">
      {#each diff.removed as line, i (i)}
        <div class="line line--removed"><span class="sign" aria-hidden="true">-</span>{line}</div>
      {/each}
      {#each diff.added as line, i (i)}
        <div class="line line--added"><span class="sign" aria-hidden="true">+</span>{line}</div>
      {/each}
      {#if diff.added.length === 0 && diff.removed.length === 0}
        <p class="muted nochange">No change: the staged edits match the record.</p>
      {/if}
    </div>
  </details>
  {#if onclose}
    <button class="button button--quiet close" onclick={onclose}>Close preview</button>
  {/if}
</section>

<style>
  .diff {
    border: 1px solid var(--rule);
    border-radius: 6px;
    padding: 0.5rem 0.75rem;
    margin: 0.75rem 0;
  }
  summary {
    cursor: pointer;
    color: var(--ink-muted);
    font-size: 0.9rem;
  }
  .lines {
    margin-top: 0.5rem;
    font-family: var(--mono);
    font-size: 0.78rem;
    overflow-x: auto;
  }
  .line {
    padding: 0.12rem 0.5rem;
    white-space: pre;
  }
  .sign {
    display: inline-block;
    width: 1.1em;
    font-weight: 700;
  }
  .line--added {
    background: #e2f2e7;
    color: #0f4722;
  }
  .line--removed {
    background: #fbe9e9;
    color: #7a1c1c;
  }
  .nochange {
    margin: 0.25rem 0;
  }
  .close {
    margin-top: 0.5rem;
    font-size: 0.8rem;
    padding: 0.25em 0.8em;
  }
</style>
