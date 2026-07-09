<script lang="ts">
  // Positional builder for the leader and 006/007/008 (tasks/049): each
  // defined position renders as a labeled input (with a datalist of known
  // values where the definition names them); everything else stays exactly
  // as the raw value holds it. The raw line is always visible underneath, so
  // nothing is hidden.
  import { fixedSlots, slotValue, withSlotValue } from "../lib/marc";

  let {
    tag,
    value,
    onchange,
  }: {
    /** "LDR", "006", "007", or "008". */
    tag: string;
    value: string;
    onchange: (value: string) => void;
  } = $props();

  // Derived, NOT computed once: the mounted instance survives a tag change
  // (a row's tag edited in place, keyed lists shifting which field lands
  // here), and a stale slot table mislabels every position and writes runs
  // at the wrong byte offsets (tasks/228).
  const slots = $derived(fixedSlots(tag));
  const listId = (i: number): string => `ffg-${tag}-${i}`;
</script>

<div class="fixed-grid">
  {#each slots as slot, i (slot.offset)}
    <label class="slot">
      <span class="slot-label">{slot.label} <span class="pos">{String(slot.offset).padStart(2, "0")}</span></span>
      <input
        class="slot-input"
        style={`width: ${Math.max(slot.length + 2, 4)}ch`}
        maxlength={slot.length}
        value={slotValue(value, slot).trimEnd()}
        list={slot.options ? listId(i) : undefined}
        onchange={(ev) => onchange(withSlotValue(value, slot, (ev.currentTarget as HTMLInputElement).value))}
      />
      {#if slot.options}
        <datalist id={listId(i)}>
          {#each slot.options as opt (opt.value)}
            <option value={opt.value}>{opt.label}</option>
          {/each}
        </datalist>
      {/if}
    </label>
  {/each}
  <label class="raw">
    <span class="slot-label">Raw</span>
    <input class="raw-input" {value} onchange={(ev) => onchange((ev.currentTarget as HTMLInputElement).value)} />
  </label>
</div>

<style>
  .fixed-grid {
    display: flex;
    gap: 0.6rem 1rem;
    flex-wrap: wrap;
    align-items: end;
    border-left: 3px solid var(--rule);
    padding: 0.35rem 0 0.35rem 0.8rem;
    margin: 0.25rem 0 0.4rem;
  }
  .slot,
  .raw {
    display: flex;
    flex-direction: column;
    gap: 0.15rem;
  }
  .slot-label {
    font-size: 0.72rem;
    color: var(--ink-muted);
  }
  .pos {
    font-family: var(--mono);
  }
  .slot-input,
  .raw-input {
    font-family: var(--mono);
    font-size: 0.85rem;
  }
  .raw {
    flex: 1;
    min-width: 16rem;
  }
  .raw-input {
    width: 100%;
  }
</style>
