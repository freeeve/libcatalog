<script lang="ts">
  // Holdings editor (tasks/051): the minimal bf:Item rows -- call number,
  // location, barcode, note -- per instance, replaced wholesale on save.
  // Circulation state never lives here. Item templates pre-fill rows and
  // bulk add generates N copies with sequential barcodes (tasks/069).
  import { onMount } from "svelte";
  import {
    ApiError,
    ConflictError,
    bulkAddItems,
    createItemTemplate,
    deleteItemTemplate,
    fetchItems,
    fetchItemTemplates,
    putItems,
    updateItemTemplate,
  } from "../lib/api";
  import { canAdmin } from "../lib/auth";
  import { isReadOnly } from "../lib/config";
  import { sessionStore } from "../lib/stores";
  import type { ItemTemplate, WorkItem } from "../lib/types";

  let { workId, instanceId }: { workId: string; instanceId: string } = $props();

  const readOnly = isReadOnly();
  const me = $derived($sessionStore?.email ?? "");
  const isAdmin = $derived(canAdmin($sessionStore));

  let items = $state<WorkItem[]>([]);
  // The etag of the grain this list was read from. The save is a whole-list
  // replacement, so writing without it deletes whatever another cataloger added
  // while this panel was open (tasks/273).
  let etag = $state("");
  let dirty = $state(false);
  let busy = $state(false);
  let status = $state("");
  let error = $state("");
  let templates = $state<ItemTemplate[]>([]);
  let templateId = $state("");
  let bulkCount = $state(2);
  let bulkPrefix = $state("");
  let bulkWidth = $state<number | undefined>(undefined);
  let bulkPreview = $state<WorkItem[]>([]);

  const template = $derived(templates.find((t) => t.id === templateId) ?? null);
  // The owner may edit or remove a template; an admin may manage a shared one
  // as its custodian, so an orphaned library template stays reachable (the
  // server enforces the same rule -- this shows only the controls it honours).
  // A personal template from a colleague is apply-only. Mirrors Macros.svelte.
  const canManageTemplate = $derived(template != null && (template.owner === me || (isAdmin && !!template.shared)));

  onMount(() => {
    void load();
    fetchItemTemplates().then(
      (r) => (templates = r.templates ?? []),
      () => {},
    );
  });

  /** Loads the list and the token the save writes back under (tasks/273). */
  async function load(): Promise<void> {
    try {
      const res = await fetchItems(workId);
      items = res.items?.[instanceId] ?? [];
      etag = res.etag ?? "";
      dirty = false;
    } catch {
      items = [];
      etag = "";
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

  /** Pushes a row pre-filled from the selected template. */
  function applyTemplate(): void {
    if (!template) return;
    items = [
      ...items,
      {
        callNumber: template.callNumber ?? "",
        location: template.location ?? "",
        barcode: "",
        note: template.note ?? "",
      },
    ];
    if (template.barcodePrefix) bulkPrefix = template.barcodePrefix;
    // The width rides into the form the way the prefix does, so it survives
    // clearing the select and is re-savable (tasks/293).
    bulkWidth = template.barcodeWidth;
    dirty = true;
  }

  /** Saves the last row's fields as a reusable template. */
  async function saveAsTemplate(shared: boolean): Promise<void> {
    const last = items[items.length - 1];
    if (!last) return;
    const label = prompt("Template label?");
    if (!label) return;
    error = "";
    try {
      const created = await createItemTemplate({
        label,
        callNumber: last.callNumber,
        location: last.location,
        note: last.note,
        barcodePrefix: bulkPrefix || undefined,
        barcodeWidth: bulkWidth || undefined,
        shared,
      });
      templates = [...templates, created];
      templateId = created.id ?? "";
      status = `template "${label}" saved${shared ? " (shared)" : ""}`;
    } catch (e) {
      error = e instanceof ApiError ? e.message : "saving the template failed";
    }
  }

  /** Renames the selected owned template (edit lifecycle, tasks/293). */
  async function renameTemplate(): Promise<void> {
    if (!template || !canManageTemplate) return;
    const label = prompt("Rename template", template.label)?.trim();
    if (!label || label === template.label) return;
    error = "";
    try {
      const updated = await updateItemTemplate(template.id ?? "", { ...template, label });
      templates = templates.map((t) => (t.id === updated.id ? updated : t));
      status = `template renamed to "${label}"`;
    } catch (e) {
      error = e instanceof ApiError ? (e.status === 403 ? "only the owner or an admin can edit a shared template" : e.message) : "rename failed";
    }
  }

  /** Removes the selected owned template (tasks/293; calls the long-dead
      deleteItemTemplate). */
  async function removeTemplate(): Promise<void> {
    if (!template || !canManageTemplate) return;
    if (!confirm(`Delete the item template "${template.label}"?`)) return;
    const id = template.id ?? "";
    const label = template.label;
    error = "";
    try {
      await deleteItemTemplate(id);
      templates = templates.filter((t) => t.id !== id);
      templateId = "";
      status = `template "${label}" deleted`;
    } catch (e) {
      error = e instanceof ApiError ? (e.status === 403 ? "only the owner or an admin can delete a shared template" : e.message) : "delete failed";
    }
  }

  async function bulk(dryRun: boolean): Promise<void> {
    // Executing a bulk add refetches the list, which would silently drop any
    // unsaved manual rows/edits -- refuse until they are saved (tasks/114).
    if (!dryRun && dirty) {
      error = "save (or remove) the pending item edits before bulk adding";
      return;
    }
    busy = true;
    error = "";
    status = "";
    try {
      const res = await bulkAddItems(workId, {
        instanceId,
        count: bulkCount,
        barcodePrefix: bulkPrefix,
        barcodeWidth: bulkWidth || undefined,
        callNumber: template?.callNumber ?? items[items.length - 1]?.callNumber ?? "",
        location: template?.location ?? items[items.length - 1]?.location ?? "",
        note: template?.note ?? "",
        dryRun,
      });
      if (dryRun) {
        bulkPreview = res.items ?? [];
      } else {
        bulkPreview = [];
        status = `added ${res.items.length} copies (${res.items[0]?.barcode}…${res.items[res.items.length - 1]?.barcode})`;
        await load();
      }
    } catch (e) {
      error = e instanceof ApiError ? e.message : "bulk add failed";
    } finally {
      busy = false;
    }
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
      await putItems(workId, instanceId, cleaned, etag);
      status = `saved ${cleaned.length} item${cleaned.length === 1 ? "" : "s"}`;
      await load();
    } catch (e) {
      if (e instanceof ConflictError) {
        // Somebody else edited this record's holdings while the panel was open.
        // Reload rather than overwrite: their copy is a physical book on a
        // shelf, and this save would have unlinked it (tasks/273).
        const mine = items.length;
        await load();
        error = `another cataloger changed this record's items while you were editing. Your ${mine} row${mine === 1 ? "" : "s"} were not saved; the list below is theirs. Re-apply your changes and save again.`;
        dirty = false;
        return;
      }
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
      {#if !readOnly}
        <button class="button button--quiet mini" onclick={() => remove(i)}>Remove</button>
      {/if}
    </div>
  {/each}
  {#if !readOnly}
    <p class="acts">
      <button class="button button--quiet mini" onclick={add}>Add item</button>
      {#if templates.length > 0}
        <select class="mini-select" aria-label="Item template" bind:value={templateId}>
          <option value="">template…</option>
          {#each templates as t (t.id)}
            <option value={t.id}>{t.label}{t.shared ? " (shared)" : ""}</option>
          {/each}
        </select>
        <button class="button button--quiet mini" onclick={applyTemplate} disabled={!template}>Apply</button>
        {#if canManageTemplate}
          <button class="button button--quiet mini" onclick={() => void renameTemplate()}>Rename</button>
          <button class="button button--quiet mini" onclick={() => void removeTemplate()}>Delete</button>
        {/if}
      {/if}
      {#if items.length > 0}
        <button class="button button--quiet mini" onclick={() => void saveAsTemplate(false)}>Save row as template</button>
        <button class="button button--quiet mini" onclick={() => void saveAsTemplate(true)}>…shared</button>
      {/if}
      <button class="button mini" onclick={() => void save()} disabled={busy || !dirty}>Save items</button>
      <span aria-live="polite">
        {#if status}<span class="ok">{status}</span>{/if}
        {#if error}<span class="error">{error}</span>{/if}
      </span>
    </p>
    <p class="acts bulk">
      <span class="muted">Bulk add</span>
      <input class="count" type="number" min="1" max="100" aria-label="Copy count" bind:value={bulkCount} />
      <input class="bc mono" aria-label="Barcode prefix" bind:value={bulkPrefix} placeholder="barcode prefix (B-)" />
      <input class="width" type="number" min="0" max="12" aria-label="Barcode number width" bind:value={bulkWidth} placeholder="width" title="Zero-padded counter width, e.g. 6 -> B-000001" />
      <button class="button button--quiet mini" onclick={() => void bulk(true)} disabled={busy || !bulkPrefix || bulkCount < 1}>
        Preview barcodes
      </button>
      {#if bulkPreview.length > 0}
        <span class="mono preview">{bulkPreview.map((it) => it.barcode).join(" ")}</span>
        <button class="button mini" onclick={() => void bulk(false)} disabled={busy}>
          Add {bulkPreview.length} copies
        </button>
      {/if}
    </p>
  {/if}
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
    flex-wrap: wrap;
  }
  .bulk {
    font-size: 0.85rem;
  }
  .count {
    width: 4rem;
  }
  .width {
    width: 4.5rem;
  }
  .mini-select {
    font-size: 0.8rem;
  }
  .preview {
    font-size: 0.75rem;
    color: var(--ink-muted);
    word-break: break-all;
  }
  .ok {
    color: var(--accent);
  }
</style>
