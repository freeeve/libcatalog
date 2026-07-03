<script lang="ts">
  // The "?" overlay: lists the bindings active right now (top scope plus
  // global). Mounted once by App; keyboard.ts calls the presenter to open it.
  import { onMount } from "svelte";
  import { formatKey, GLOBAL_SCOPE, setHelpPresenter, type Binding } from "../lib/keyboard";
  import Modal from "./Modal.svelte";

  let open = $state(false);
  let active = $state<Binding[]>([]);

  const local = $derived(active.filter((b) => b.scope !== GLOBAL_SCOPE));
  const global = $derived(active.filter((b) => b.scope === GLOBAL_SCOPE));

  onMount(() => {
    setHelpPresenter((bindings) => {
      active = bindings;
      open = true;
    });
    return () => setHelpPresenter(null);
  });

  function close(): void {
    open = false;
  }
</script>

{#if open}
  <Modal ariaLabel="Keyboard shortcuts" onclose={close} width="28rem">
    <h2>Keyboard shortcuts</h2>
    {#if local.length > 0}
      <h3>This screen</h3>
      <dl>
        {#each local as b (b.key)}
          <div class="row">
            <dt><kbd>{formatKey(b)}</kbd></dt>
            <dd>{b.description}</dd>
          </div>
        {/each}
      </dl>
    {/if}
    <h3>Everywhere</h3>
    <dl>
      {#each global as b (b.key)}
        <div class="row">
          <dt><kbd>{formatKey(b)}</kbd></dt>
          <dd>{b.description}</dd>
        </div>
      {/each}
      <div class="row">
        <dt><kbd>?</kbd></dt>
        <dd>show this help</dd>
      </div>
    </dl>
    <button class="button button--quiet" onclick={close}>Close</button>
  </Modal>
{/if}

<style>
  h2 {
    margin-top: 0;
  }
  h3 {
    font-size: 0.78rem;
    font-weight: 650;
    text-transform: uppercase;
    letter-spacing: 0.07em;
    color: var(--ink-muted);
    margin: 0.8rem 0 0.2rem;
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
