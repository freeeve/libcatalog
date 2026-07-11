<script lang="ts">
  // The "?" overlay: lists the bindings active right now (top scope plus
  // global) and lets the user redefine them -- pick a binding,
  // press the new chord, conflicts against the binding's scope plus global
  // are refused with the holder named, reserved chords with the reason.
  // Remaps persist via the keyboard registry's keymap; presets apply as a
  // bundle. Mounted once by App; keyboard.ts calls the presenter to open it.
  import { onMount } from "svelte";
  import {
    activeBindings,
    conflictingBinding,
    formatKey,
    GLOBAL_SCOPE,
    normalizeChord,
    resetKeymap,
    reservedReason,
    setHelpPresenter,
    setKeymapEntry,
    type Binding,
  } from "../lib/keyboard";
  import { applyPreset, PRESETS } from "../lib/keymaps";
  import Modal from "./Modal.svelte";

  let open = $state(false);
  let active = $state<Binding[]>([]);
  let editing = $state<string | null>(null); // action id capturing a chord
  let notice = $state("");
  let presetName = $state("default");

  const local = $derived(active.filter((b) => b.scope !== GLOBAL_SCOPE));
  const global = $derived(active.filter((b) => b.scope === GLOBAL_SCOPE));
  const remapCount = $derived(active.filter((b) => remapped(b)).length);

  onMount(() => {
    setHelpPresenter((bindings) => {
      active = bindings;
      editing = null;
      notice = "";
      open = true;
    });
    return () => setHelpPresenter(null);
  });

  function refresh(): void {
    active = activeBindings().filter((b) => !b.hidden);
  }

  function close(): void {
    editing = null;
    open = false;
  }

  function remapped(b: Binding): boolean {
    return b.defaultKey !== undefined && b.key !== b.defaultKey;
  }

  /** Capture-phase chord capture while a binding is being redefined: the
   *  next non-modifier keydown becomes the candidate; Escape cancels. */
  function onCapture(ev: KeyboardEvent): void {
    if (!editing) return;
    ev.preventDefault();
    ev.stopPropagation();
    if (ev.key === "Meta" || ev.key === "Control" || ev.key === "Alt" || ev.key === "Shift") return;
    if (ev.key === "Escape") {
      editing = null;
      notice = "";
      return;
    }
    const id = editing;
    const chord = normalizeChord(ev);
    editing = null;
    const reserved = reservedReason(chord);
    if (reserved) {
      notice = `"${formatKey({ key: chord })}" is reserved: ${reserved}.`;
      return;
    }
    const holder = conflictingBinding(id, chord);
    if (holder) {
      notice = `"${formatKey({ key: chord })}" already runs "${holder.description}".`;
      return;
    }
    if (setKeymapEntry(id, chord)) {
      notice = `Remapped to ${formatKey({ key: chord })}.`;
      refresh();
    }
  }

  function beginEdit(b: Binding): void {
    if (!b.id) return;
    editing = b.id;
    notice = "";
  }

  function reset(b: Binding): void {
    if (b.id && setKeymapEntry(b.id, null)) {
      notice = `${b.description}: default restored.`;
      refresh();
    } else {
      notice = "The default key is taken by another remap -- reset that one first.";
    }
  }

  function resetAll(): void {
    resetKeymap();
    notice = "All bindings restored to defaults.";
    refresh();
  }

  function usePreset(): void {
    const skipped = applyPreset(presetName);
    const p = PRESETS.find((x) => x.name === presetName);
    notice =
      `Applied the ${p?.label ?? presetName} preset.` +
      (skipped.length ? ` ${skipped.length} binding${skipped.length === 1 ? "" : "s"} kept previous keys (conflicts).` : "");
    refresh();
  }
</script>

<svelte:window onkeydowncapture={onCapture} />

{#if open}
  <Modal ariaLabel="Keyboard shortcuts" onclose={close} width="34rem">
    <h2>Keyboard shortcuts</h2>
    <p class="muted meta" aria-live="polite">
      {#if editing}
        Press the new chord ({formatKey({ key: "Escape" })} cancels)…
      {:else if notice}
        {notice}
      {:else}
        Click a key to redefine it. Remaps persist in this browser{remapCount > 0 ? ` (${remapCount} active)` : ""}.
      {/if}
    </p>
    {#if local.length > 0}
      <h3>This screen</h3>
      <dl>
        {#each local as b (b.id ?? b.key)}
          <div class="row">
            <dt>
              <button class="kbd-btn" class:capturing={editing === b.id} onclick={() => beginEdit(b)} title="Redefine this key">
                <kbd>{editing === b.id ? "…" : formatKey(b)}</kbd>
              </button>
            </dt>
            <dd>
              {b.description}
              {#if remapped(b)}
                <button class="button button--quiet mini" onclick={() => reset(b)}>reset ({formatKey({ key: b.defaultKey ?? "" })})</button>
              {/if}
            </dd>
          </div>
        {/each}
      </dl>
    {/if}
    <h3>Everywhere</h3>
    <dl>
      {#each global as b (b.id ?? b.key)}
        <div class="row">
          <dt>
            <button class="kbd-btn" class:capturing={editing === b.id} onclick={() => beginEdit(b)} title="Redefine this key">
              <kbd>{editing === b.id ? "…" : formatKey(b)}</kbd>
            </button>
          </dt>
          <dd>
            {b.description}
            {#if remapped(b)}
              <button class="button button--quiet mini" onclick={() => reset(b)}>reset ({formatKey({ key: b.defaultKey ?? "" })})</button>
            {/if}
          </dd>
        </div>
      {/each}
      <div class="row">
        <dt><kbd class="fixed">?</kbd></dt>
        <dd>show this help</dd>
      </div>
    </dl>
    <div class="foot">
      <label class="muted preset">
        Preset
        <select bind:value={presetName}>
          {#each PRESETS as p (p.name)}
            <option value={p.name}>{p.label}</option>
          {/each}
        </select>
      </label>
      <button class="button button--quiet" onclick={usePreset}>Apply preset</button>
      <button class="button button--quiet" onclick={resetAll}>Reset all</button>
      <button class="button" onclick={close}>Close</button>
    </div>
    <p class="muted note">{PRESETS.find((p) => p.name === presetName)?.description}</p>
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
    min-width: 5.5rem;
  }
  dd {
    margin: 0;
    color: var(--ink-muted);
    display: flex;
    gap: 0.5rem;
    align-items: baseline;
    flex-wrap: wrap;
  }
  .kbd-btn {
    background: none;
    border: 0;
    padding: 0;
    cursor: pointer;
  }
  .kbd-btn.capturing kbd {
    border-color: var(--accent);
    color: var(--accent);
  }
  kbd {
    font-family: var(--mono);
    font-size: 0.85em;
    background: var(--surface);
    border: 1px solid var(--rule);
    border-radius: 4px;
    padding: 0.05em 0.5em;
  }
  .meta {
    font-size: 0.8rem;
    min-height: 1.2em;
  }
  .mini {
    font-size: 0.7rem;
    padding: 0 0.5em;
  }
  .foot {
    display: flex;
    gap: 0.6rem;
    align-items: center;
    flex-wrap: wrap;
  }
  .preset {
    display: inline-flex;
    gap: 0.4rem;
    align-items: center;
    font-size: 0.85rem;
  }
  .note {
    font-size: 0.75rem;
    margin: 0.5rem 0 0;
  }
</style>
