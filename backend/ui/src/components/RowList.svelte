<script lang="ts" generics="T">
  // The one keyboard list: compact rows with the inset selection rail,
  // clamped j/k movement, and scroll-into-view. Screens render row content
  // through the snippet and keep their own action keys; when a keyboard
  // scope is given the nav keys (and Enter, if onactivate exists) register
  // there with footer legends. Row look comes from the shared .rowlist CSS
  // in app.css so lists match everywhere.
  import { onMount, type Snippet } from "svelte";
  import { bindKeys, type BindingSpec } from "../lib/keyboard";

  let {
    items,
    selected = $bindable(0),
    getKey,
    ariaLabel,
    onactivate,
    scope,
    itemName = "row",
    row,
    empty,
  }: {
    items: T[];
    selected?: number;
    getKey: (item: T, i: number) => string | number;
    ariaLabel: string;
    onactivate?: (item: T, i: number) => void;
    scope?: string;
    itemName?: string;
    row: Snippet<[T, number, boolean]>;
    empty?: Snippet | string;
  } = $props();

  let listEl = $state<HTMLElement | null>(null);

  /** Moves the selection by delta, clamped, keeping the row in view. */
  export function move(delta: number): void {
    if (items.length === 0) return;
    selected = Math.min(items.length - 1, Math.max(0, selected + delta));
    listEl?.querySelectorAll(":scope > li")[selected]?.scrollIntoView?.({ block: "nearest" });
  }

  /** Fires onactivate for the selected row. */
  export function activate(): void {
    const it = items[selected];
    if (it !== undefined && onactivate) onactivate(it, selected);
  }

  onMount(() => {
    if (!scope) return;
    const specs: Record<string, BindingSpec> = {
      j: { description: `next ${itemName}`, legend: "move", keyLabel: "j/k", handler: () => move(1) },
      k: { description: `previous ${itemName}`, hidden: true, handler: () => move(-1) },
      ArrowDown: { description: `next ${itemName}`, hidden: true, handler: () => move(1) },
      ArrowUp: { description: `previous ${itemName}`, hidden: true, handler: () => move(-1) },
    };
    if (onactivate) {
      specs.Enter = { description: `open the selected ${itemName}`, legend: "open", handler: () => activate() };
    }
    return bindKeys(scope, specs);
  });
</script>

<ul class="rowlist" bind:this={listEl} aria-label={ariaLabel}>
  {#each items as it, i (getKey(it, i))}
    <li class:selected={i === selected} onfocusin={() => (selected = i)}>
      {@render row(it, i, i === selected)}
    </li>
  {:else}
    {#if typeof empty === "string"}
      <li class="rowlist-empty muted">{empty}</li>
    {:else if empty}
      <li class="rowlist-empty">{@render empty()}</li>
    {/if}
  {/each}
</ul>
