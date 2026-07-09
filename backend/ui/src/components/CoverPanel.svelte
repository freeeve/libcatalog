<script lang="ts">
  // Cover art for one work (tasks/215): shows the current cover (the
  // editorial-or-feed extra.cover the OPAC's cover slot reads), uploads a
  // replacement, or removes the editorial one. Writes are immediate (like
  // items), not staged ops.
  import { apiBase } from "../lib/config";
  import { deleteCover, putCover, humanApiMessage } from "../lib/api";
  import { isReadOnly } from "../lib/config";

  let { workId, cover = "" }: { workId: string; cover?: string } = $props();

  const readOnly = isReadOnly();
  // The prop is the doc's load-time value; local writes take over after.
  // svelte-ignore state_referenced_locally
  let current = $state(cover);
  let busy = $state(false);
  let error = $state("");
  let bump = $state(0); // cache-buster after replace

  const src = $derived(current ? `${apiBase()}/${current}?v=${bump}` : "");

  async function upload(ev: Event): Promise<void> {
    const input = ev.currentTarget as HTMLInputElement;
    const file = input.files?.[0];
    input.value = "";
    if (!file) return;
    busy = true;
    error = "";
    try {
      const res = await putCover(workId, file);
      current = res.cover;
      bump++;
    } catch (e) {
      error = humanApiMessage(e, "cover upload failed");
    } finally {
      busy = false;
    }
  }

  async function remove(): Promise<void> {
    busy = true;
    error = "";
    try {
      await deleteCover(workId);
      current = "";
    } catch (e) {
      error = humanApiMessage(e, "cover removal failed");
    } finally {
      busy = false;
    }
  }
</script>

<section class="coverpanel" aria-label="Cover art">
  <h3>Cover</h3>
  {#if current}
    <img class="cover" {src} alt="" width="100" height="150" />
  {:else}
    <p class="muted">none</p>
  {/if}
  {#if !readOnly}
    <p class="acts">
      <label class="button button--quiet">
        {current ? "Replace…" : "Upload…"}
        <input type="file" accept="image/jpeg,image/png,image/webp" onchange={upload} disabled={busy} hidden />
      </label>
      {#if current}
        <button class="button button--quiet" onclick={remove} disabled={busy}>Remove</button>
      {/if}
    </p>
  {/if}
  {#if error}<p class="error" role="alert">{error}</p>{/if}
</section>

<style>
  .coverpanel h3 {
    margin: 0 0 0.3rem;
    font-size: 0.72rem;
    font-weight: 650;
    text-transform: uppercase;
    letter-spacing: 0.07em;
    color: var(--ink-muted);
  }
  .cover {
    display: block;
    border: 1px solid var(--rule);
    border-radius: 4px;
    object-fit: cover;
  }
  .acts {
    display: flex;
    gap: 0.4rem;
    margin: 0.4rem 0 0;
  }
  label.button input {
    display: none;
  }
</style>
