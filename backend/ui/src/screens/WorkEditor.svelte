<script lang="ts">
  // Read-only work editor (task 043): fetches the WorkDoc and renders it via
  // WorkDocView. Editing lands in a later task.
  import { fetchWorkDoc, ApiError } from "../lib/api";
  import WorkDocView from "../components/WorkDocView.svelte";

  let { workId }: { workId: string } = $props();
</script>

<main>
  <p><a href="#/works">← Back to search</a></p>
  {#await fetchWorkDoc(workId)}
    <p class="muted" aria-live="polite">Loading {workId}…</p>
  {:then res}
    <WorkDocView doc={res.doc} etag={res.etag} />
  {:catch e}
    <p class="error" role="alert">
      {e instanceof ApiError && e.status === 404 ? `No work ${workId}.` : `Failed to load ${workId}: ${e.message}`}
    </p>
  {/await}
</main>
