<script lang="ts">
  // Holdings editor (tasks/051): the minimal bf:Item rows -- call number,
  // location, barcode, note -- per instance, replaced wholesale on save.
  // Circulation state never lives here.
  import { onMount } from "svelte";
  import { fetchItems, putItems, ApiError } from "../lib/api";
  import type { WorkItem } from "../lib/types";

  let { workId, instanceId }: { workId: string; instanceId: string } = $props();

  let items = $state<WorkItem[]>([]);
  let dirty = $state(false);
  let busy = $state(false);
  let status = $state("");
  let error = $state("");

  onMount(() => void load());

  async function load(): Promise<void> {
    try {
      const res = await fetchItems(workId);
      items = res.items?.[instanceId] ?? [];
      dirty = false;
    } catch {
      items = [];
    }
  }

  function edit(i: number, patch: Partial<WorkItem>): void {
    items = items.map((it, j) => (j === i ? { ...it, ...patch } : it));
    dirty = true;
  }

  function add(): void {
    items = [...items, { callNumber: "", location: "", barcode: "", note: "" }];
    dirty = true;
  }

  function remove(i: number): void {
    items = items.filter((_, j) => j !== i);
    dirty = true;
  }

  async function save(): Promise<void> {
    busy = true;
    error = "";
    status = "";
    try {
      const cleaned = items
        .map(({ callNumber, location, barcode, note }) => ({ callNumber, location, barcode, note }))
        .filter((it) => it.callNumber || it.location || it.barcode || it.note);
      await putItems(workId, instanceId, cleaned);
      status = `saved ${cleaned.length} item${cleaned.length === 1 ? "" : "s"}`;
      await load();
    } catch (e) {
      error = e instanceof ApiError ? e.message : "saving items failed";
    } finally {
      busy = false;
    }
  }
</script>

<details class="items">
  <summary>Items ({items.length})</summary>
  {#each items as it, i (i)}
    <div class="row">
      <input class="cn" aria-label="Call number" value={it.callNumber ?? ""} placeholder="call number"
        oninput={(ev) => edit(i, { callNumber: (ev.currentTarget as HTMLInputElement).value })} />
      <input class="loc" aria-label="Location" value={it.location ?? ""} placeholder="location"
        oninput={(ev) => edit(i, { location: (ev.currentTarget as HTMLInputElement).value })} />
      <input class="bc mono" aria-label="Barcode" value={it.barcode ?? ""} placeholder="barcode"
        oninput={(ev) => edit(i, { barcode: (ev.currentTarget as HTMLInputElement).value })} />
      <input class="note" aria-label="Note" value={it.note ?? ""} placeholder="note"
        oninput={(ev) => edit(i, { note: (ev.currentTarget as HTMLInputElement).value })} />
      <button class="button button--quiet mini" onclick={() => remove(i)}>Remove</button>
    </div>
  {/each}
  <p class="acts">
    <button class="button button--quiet mini" onclick={add}>Add item</button>
    <button class="button mini" onclick={() => void save()} disabled={busy || !dirty}>Save items</button>
    <span aria-live="polite">
      {#if status}<span class="ok">{status}</span>{/if}
      {#if error}<span class="error">{error}</span>{/if}
    </span>
  </p>
</details>

<style>
  .items {
    margin: 0.4rem 0;
  }
  .items summary {
    cursor: pointer;
    color: var(--ink-muted);
    font-size: 0.88rem;
  }
  .row {
    display: flex;
    gap: 0.4rem;
    margin: 0.3rem 0;
    flex-wrap: wrap;
  }
  .cn {
    width: 9rem;
  }
  .loc {
    width: 11rem;
  }
  .bc {
    width: 8rem;
  }
  .note {
    flex: 1;
    min-width: 8rem;
  }
  .mono {
    font-family: var(--mono);
  }
  .mini {
    font-size: 0.75rem;
    padding: 0.08em 0.7em;
  }
  .acts {
    display: flex;
    gap: 0.5rem;
    align-items: center;
  }
  .ok {
    color: var(--accent);
  }
</style>
