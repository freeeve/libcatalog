<script lang="ts">
  // The "?" overlay: lists the bindings active right now (top scope plus
  // global). Mounted once by App; keyboard.ts calls the presenter to open it.
  import { onMount } from "svelte";
  import { setHelpPresenter, type Binding } from "../lib/keyboard";

  let open = $state(false);
  let active = $state<Binding[]>([]);
  let panel = $state<HTMLElement | null>(null);

  onMount(() => {
    setHelpPresenter((bindings) => {
      active = bindings;
      open = true;
    });
    return () => setHelpPresenter(null);
  });

  $effect(() => {
    if (open) panel?.focus();
  });

  function close(): void {
    open = false;
  }

  function onKeydown(ev: KeyboardEvent): void {
    if (ev.key === "Escape") close();
  }
</script>

<svelte:window onkeydown={open ? onKeydown : undefined} />

{#if open}
  <div class="scrim">
    <div class="panel" role="dialog" aria-modal="true" aria-label="Keyboard shortcuts" tabindex="-1" bind:this={panel}>
      <h2>Keyboard shortcuts</h2>
      {#if active.length === 0}
        <p class="muted">No shortcuts on this screen.</p>
      {:else}
        <dl>
          {#each active as b (b.key)}
            <div class="row">
              <dt><kbd>{b.key}</kbd></dt>
              <dd>{b.description}</dd>
            </div>
          {/each}
          <div class="row">
            <dt><kbd>?</kbd></dt>
            <dd>show this help</dd>
          </div>
        </dl>
      {/if}
      <button class="button button--quiet" onclick={close}>Close</button>
    </div>
  </div>
{/if}

<style>
  .scrim {
    position: fixed;
    inset: 0;
    background: rgba(20, 22, 25, 0.55);
    display: grid;
    place-items: center;
    z-index: 50;
  }
  .panel {
    background: var(--bg);
    border: 1px solid var(--rule);
    border-radius: 8px;
    padding: 1.25rem 1.5rem;
    min-width: 20rem;
    max-width: 90vw;
    max-height: 80vh;
    overflow-y: auto;
  }
  .panel h2 {
    margin-top: 0;
  }
  dl {
    margin: 0 0 1rem;
  }
  .row {
    display: flex;
    gap: 0.9rem;
    align-items: baseline;
    padding: 0.2rem 0;
  }
  dt {
    min-width: 4rem;
  }
  dd {
    margin: 0;
    color: var(--ink-muted);
  }
  kbd {
    font-family: var(--mono);
    font-size: 0.85em;
    background: var(--surface);
    border: 1px solid var(--rule);
    border-radius: 4px;
    padding: 0.05em 0.5em;
  }
</style>
