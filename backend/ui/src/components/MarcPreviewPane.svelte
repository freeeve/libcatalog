<script lang="ts">
  // Read-only live MARC preview: the staged native ops applied
  // server-side to the current doc and re-encoded as MARC, refreshed
  // debounced as edits stage or unstage. Lines differing from the saved
  // state highlight; known-loss tags render muted with the fidelity reason,
  // exactly as the editing grid marks them.
  import { onMount } from "svelte";
  import { marcPreview } from "../lib/api";
  import { sequencer } from "../lib/sequence";
  import MarcRecordView from "./MarcRecordView.svelte";
  import type { MarcField, MarcRecordDoc, Op } from "../lib/types";

  let { workId, ops }: { workId: string; ops: Op[] } = $props();

  const DEBOUNCE_MS = 400;
  const seq = sequencer();

  let records = $state<MarcRecordDoc[]>([]);
  let knownLoss = $state<Record<string, string>>({});
  let baseline = $state<Set<string>>(new Set());
  let seeded = $state(false);
  let loading = $state(true);
  let error = $state("");
  let timer: ReturnType<typeof setTimeout> | undefined;

  function line(f: MarcField): string {
    if (f.value !== undefined && f.value !== "") return `${f.tag} ${f.value}`;
    const subs = (f.subfields ?? []).map((sf) => `$${sf.code} ${sf.value}`).join(" ");
    return `${f.tag} ${f.ind1 ?? " "}${f.ind2 ?? " "} ${subs}`;
  }

  function changed(rec: MarcRecordDoc, f: MarcField): boolean {
    return seeded && !baseline.has(rec.node + "|" + line(f));
  }

  onMount(() => {
    // The saved state seeds the highlight baseline; the current staged ops
    // render on top of it.
    void seed().then(() => void refresh());
    return () => clearTimeout(timer);
  });

  // Re-preview (debounced) whenever the staged op list changes.
  $effect(() => {
    void ops;
    if (!seeded) return;
    clearTimeout(timer);
    timer = setTimeout(() => void refresh(), DEBOUNCE_MS);
  });

  async function seed(): Promise<void> {
    try {
      const res = await marcPreview(workId, []);
      const lines = new Set<string>();
      for (const rec of res.records ?? []) {
        for (const f of rec.fields) lines.add(rec.node + "|" + line(f));
      }
      baseline = lines;
      knownLoss = res.knownLoss ?? {};
    } catch {
      // The refresh below reports the error; an unseeded baseline just
      // means no highlighting.
    } finally {
      seeded = true;
    }
  }

  async function refresh(): Promise<void> {
    const t = seq.take();
    try {
      const res = await marcPreview(workId, ops);
      if (t.stale) return;
      records = res.records ?? [];
      error = "";
    } catch {
      if (t.stale) return;
      error = "MARC preview unavailable";
    } finally {
      if (!t.stale) loading = false;
    }
  }
</script>

<div class="pane" aria-label="Live MARC preview">
  <p class="head muted">
    MARC preview <span class="ro">read-only</span>
    {#if loading}<span role="status">loading…</span>{/if}
    {#if error}<span class="error" role="alert">{error}</span>{/if}
  </p>
  {#each records as rec, i (rec.node)}
    {#if records.length > 1}<p class="muted rechead">Record {i + 1}</p>{/if}
    <MarcRecordView record={rec} {knownLoss} changed={(f) => changed(rec, f)} />
  {:else}
    {#if !loading && !error}<p class="muted">This work decodes to no MARC records.</p>{/if}
  {/each}
</div>

<style>
  .pane {
    border: 1px solid var(--rule);
    border-radius: 8px;
    padding: 0.5rem 0.8rem 0.7rem;
    max-height: 75vh;
    overflow-y: auto;
  }
  .head {
    display: flex;
    gap: 0.6rem;
    align-items: baseline;
    font-size: 0.8rem;
    margin: 0.2rem 0 0.5rem;
  }
  .ro {
    font-size: 0.68rem;
    text-transform: uppercase;
    letter-spacing: 0.04em;
    border: 1px solid var(--rule);
    border-radius: 999px;
    padding: 0.05em 0.6em;
  }
  .rechead {
    font-size: 0.8rem;
    margin: 0.5rem 0 0.2rem;
  }
</style>
