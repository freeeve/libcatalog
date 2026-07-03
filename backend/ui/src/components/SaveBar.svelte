<script lang="ts">
  // Sticky action bar over the staged op list: count, the dry-run preview,
  // the If-Match save, and discard (PublishBar's editing sibling).
  let {
    count,
    busy,
    onpreview,
    onsave,
    ondiscard,
  }: {
    count: number;
    busy: boolean;
    onpreview: () => void;
    onsave: () => void;
    ondiscard: () => void;
  } = $props();
</script>

{#if count > 0}
  <div class="bar" role="region" aria-label="Staged edits">
    <span class="counts"><strong>{count}</strong> staged edit{count === 1 ? "" : "s"}</span>
    <span class="spacer"></span>
    <button class="button button--quiet" onclick={ondiscard} disabled={busy}>Discard</button>
    <button class="button button--quiet" onclick={onpreview} disabled={busy}>Preview changes</button>
    <button class="button" onclick={onsave} disabled={busy}>Save</button>
  </div>
{/if}

<style>
  .bar {
    position: sticky;
    bottom: 0;
    display: flex;
    align-items: center;
    gap: 0.6rem;
    padding: 0.6rem 0.9rem;
    margin-top: 1rem;
    background: var(--bg);
    border: 1px solid var(--rule);
    border-radius: 8px 8px 0 0;
    box-shadow: 0 -4px 14px rgba(20, 22, 25, 0.08);
  }
  .spacer {
    flex: 1;
  }
</style>
