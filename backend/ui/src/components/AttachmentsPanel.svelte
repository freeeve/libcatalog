<script lang="ts">
  // Staff attachments for one work (tasks/229, 058 item 2): scans,
  // correspondence, acquisition paperwork -- blob-stored working material,
  // never projected publicly. Writes are immediate (like items and covers),
  // not staged ops. Downloads go through fetch so the bearer rides along.
  import { onMount } from "svelte";
  import { deleteAttachment, fetchAttachmentBlob, fetchAttachments, humanApiMessage, putAttachment, safeAttachmentName } from "../lib/api";
  import { isReadOnly } from "../lib/config";

  let { workId }: { workId: string } = $props();

  const readOnly = isReadOnly();
  let names = $state<string[]>([]);
  let busy = $state(false);
  let error = $state("");

  async function load(): Promise<void> {
    try {
      names = (await fetchAttachments(workId)).attachments ?? [];
    } catch (e) {
      error = humanApiMessage(e, "loading attachments failed");
    }
  }

  async function upload(ev: Event): Promise<void> {
    const input = ev.currentTarget as HTMLInputElement;
    const file = input.files?.[0];
    input.value = "";
    if (!file) return;
    const name = safeAttachmentName(file.name);
    if (!name) {
      error = "that filename has no usable characters -- rename and retry";
      return;
    }
    busy = true;
    error = "";
    try {
      await putAttachment(workId, file, name);
      await load();
    } catch (e) {
      error = humanApiMessage(e, "attachment upload failed");
    } finally {
      busy = false;
    }
  }

  async function remove(name: string): Promise<void> {
    busy = true;
    error = "";
    try {
      await deleteAttachment(workId, name);
      await load();
    } catch (e) {
      error = humanApiMessage(e, "attachment removal failed");
    } finally {
      busy = false;
    }
  }

  async function download(name: string): Promise<void> {
    error = "";
    try {
      const blob = await fetchAttachmentBlob(workId, name);
      const url = URL.createObjectURL(blob);
      const a = document.createElement("a");
      a.href = url;
      a.download = name;
      a.click();
      URL.revokeObjectURL(url);
    } catch (e) {
      error = humanApiMessage(e, "download failed");
    }
  }

  onMount(() => void load());
</script>

<section class="attachpanel" aria-label="Attachments">
  <h3>Attachments</h3>
  <p class="muted">Staff working files (20MB each); never published to the catalog.</p>
  {#if names.length === 0}
    <p class="muted">none</p>
  {:else}
    <ul>
      {#each names as name (name)}
        <li>
          <button class="link-button" onclick={() => void download(name)} title="download">{name}</button>
          {#if !readOnly}
            <button class="button button--quiet" onclick={() => void remove(name)} disabled={busy} title="remove attachment">×</button>
          {/if}
        </li>
      {/each}
    </ul>
  {/if}
  {#if !readOnly}
    <p><input type="file" aria-label="Attachment file" onchange={(ev) => void upload(ev)} disabled={busy} /></p>
  {/if}
  {#if error}<p class="error" role="alert">{error}</p>{/if}
</section>

<style>
  .attachpanel h3 {
    margin: 0.4rem 0 0.2rem;
  }
  .attachpanel p {
    margin: 0.15rem 0;
    font-size: var(--fs-meta);
  }
  .attachpanel ul {
    margin: 0.2rem 0;
    padding-left: 1.1rem;
  }
  .attachpanel li {
    display: flex;
    align-items: baseline;
    gap: 0.5em;
  }
  .link-button {
    background: none;
    border: none;
    padding: 0;
    font: inherit;
    color: var(--accent);
    text-decoration: underline;
    cursor: pointer;
  }
</style>
