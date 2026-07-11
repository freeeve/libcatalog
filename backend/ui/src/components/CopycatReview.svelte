<script lang="ts">
  // One staged batch under keyboard review (scope "copycat-review", pushed
  // while open so the legend flips): j/k move, i/s decide import/skip,
  // A imports every "new" record, N skips everything already in the
  // catalog, o opens the matched work, v shows the selected record's MARC
  //, c commits behind a confirm modal, Escape closes the batch.
  // Decisions render as tinted chips; bulk keys ship one review call.
  import { onMount } from "svelte";
  import { ApiError, commitCopycatBatch, revertCopycatBatch, reviewCopycatBatch } from "../lib/api";
  import { isReadOnly } from "../lib/config";
  import { bindKeys, popScope, pushScope } from "../lib/keyboard";
  import { navigate } from "../lib/router";
  import MarcRecordView from "./MarcRecordView.svelte";
  import Modal from "./Modal.svelte";
  import RowList from "./RowList.svelte";
  import type { CopycatBatch, CopycatPolicy, CopycatStagedRecord } from "../lib/types";

  let {
    batch = $bindable(),
    records = $bindable(),
    onclose,
    oncommitted,
  }: {
    batch: CopycatBatch;
    records: CopycatStagedRecord[];
    onclose: () => void;
    oncommitted: (done: CopycatBatch) => void;
  } = $props();

  const SCOPE = "copycat-review";

  const POLICIES: { value: CopycatPolicy; label: string }[] = [
    { value: "replace-feed", label: "Replace feed data (editorial always kept)" },
    { value: "fill-holes-only", label: "Fill holes only (never overwrite an existing edition)" },
    { value: "never", label: "Never overlay (import only unmatched records)" },
  ];

  let selected = $state(0);
  let viewing = $state(false);
  let confirming = $state(false);
  let confirmingRevert = $state(false);
  let busy = $state(false);
  let error = $state("");
  let revertNote = $state("");
  let revertSkips = $state<{ path: string; reason: string }[]>([]);

  const readOnly = isReadOnly();
  const staged = $derived(batch.status === "STAGED");
  const committed = $derived(batch.status === "COMMITTED");
  const importCount = $derived(records.filter((r) => r.decision === "import").length);

  onMount(() => {
    pushScope(SCOPE);
    const unbind = bindKeys(SCOPE, {
      i: { description: "decide import for the selected record", legend: "import", handler: () => void decideSelected("import") },
      s: { description: "decide skip for the selected record", legend: "skip", handler: () => void decideSelected("skip") },
      A: { description: 'import every "new" record', legend: "import all new", handler: () => void decideWhere((r) => !r.match.matchedWork && !r.match.matchedInstance, "import") },
      N: { description: "skip everything already in the catalog", legend: "skip already-held", handler: () => void decideWhere((r) => !!r.match.matchedInstance, "skip") },
      o: { description: "open the selected record's matched work", legend: "open match", handler: openMatch },
      v: { description: "show or hide the selected record's MARC", legend: "view marc", handler: () => (viewing = !viewing) },
      c: { description: "commit the batch", legend: "commit", handler: () => !readOnly && staged && (confirming = true) },
      Escape: { description: "close this batch", legend: "close", handler: onclose },
    });
    return () => {
      unbind();
      popScope(SCOPE);
    };
  });

  async function setPolicy(policy: CopycatPolicy): Promise<void> {
    try {
      batch = await reviewCopycatBatch(batch.id, { policy });
    } catch (e) {
      error = e instanceof ApiError ? e.message : "updating the policy failed";
    }
  }

  async function decide(indices: number[], decision: "import" | "skip"): Promise<void> {
    if (!staged || indices.length === 0) return;
    const decisions: Record<string, "import" | "skip"> = {};
    for (const i of indices) decisions[String(i)] = decision;
    error = "";
    try {
      await reviewCopycatBatch(batch.id, { decisions });
      records = records.map((r) => (decisions[String(r.index)] ? { ...r, decision } : r));
    } catch {
      error = "updating the decision failed";
    }
  }

  function decideSelected(decision: "import" | "skip"): Promise<void> {
    const r = records[selected];
    return r ? decide([r.index], decision) : Promise.resolve();
  }

  function decideWhere(pred: (r: CopycatStagedRecord) => boolean, decision: "import" | "skip"): Promise<void> {
    return decide(records.filter(pred).map((r) => r.index), decision);
  }

  function openMatch(): void {
    const id = records[selected]?.match.workId;
    if (id) navigate(`/works/${encodeURIComponent(id)}`);
  }

  async function commit(): Promise<void> {
    confirming = false;
    busy = true;
    error = "";
    revertNote = "";
    revertSkips = [];
    try {
      const done = await commitCopycatBatch(batch.id);
      batch = done;
      oncommitted(done);
    } catch (e) {
      error = e instanceof ApiError ? e.message : "commit failed";
    } finally {
      busy = false;
    }
  }

  async function revert(): Promise<void> {
    confirmingRevert = false;
    busy = true;
    error = "";
    try {
      const res = await revertCopycatBatch(batch.id);
      batch = res.batch;
      revertSkips = res.skipped ?? [];
      revertNote = `${res.reverted} grain${res.reverted === 1 ? "" : "s"} reverted${revertSkips.length ? `, ${revertSkips.length} skipped` : ""}`;
      oncommitted(res.batch);
    } catch (e) {
      error = e instanceof ApiError ? e.message : "revert failed";
    } finally {
      busy = false;
    }
  }

  function matchLabel(r: CopycatStagedRecord): string {
    if (r.match.matchedInstance) return "already in the catalog";
    if (r.match.matchedWork) return "would merge with an existing work";
    return "new";
  }
</script>

<div class="review" aria-label={"Batch " + batch.label}>
  <header class="rhead">
    <h3>{batch.label}</h3>
    <span class="muted">{records.length} records</span>
    <button class="button button--quiet mini" onclick={onclose}>Close</button>
  </header>
  <div class="row">
    <label for="cc-policy" class="muted">Overlay policy</label>
    <select
      id="cc-policy"
      value={batch.policy}
      disabled={!staged}
      onchange={(ev) => void setPolicy((ev.currentTarget as HTMLSelectElement).value as CopycatPolicy)}
    >
      {#each POLICIES as p (p.value)}
        <option value={p.value}>{p.label}</option>
      {/each}
    </select>
  </div>
  {#if error}<p class="error" role="alert">{error}</p>{/if}

  <RowList items={records} bind:selected getKey={(r) => r.index} ariaLabel={"Records in " + batch.label} scope={SCOPE} itemName="record">
    {#snippet row(r: CopycatStagedRecord)}
      <div class="rrow" class:tint-ok={r.decision === "import"} class:tint-danger={r.decision === "skip"}>
        <span class="mono idx">{r.index + 1}</span>
        <span class="title">{r.title || "(untitled)"}</span>
        <span class="match" data-kind={r.match.matchedInstance ? "instance" : r.match.matchedWork ? "work" : "new"}>
          {matchLabel(r)}
        </span>
        <span class="link">
          {#if r.match.workId}
            <a href={"#/works/" + encodeURIComponent(r.match.workId)}>{r.match.workId}</a>
          {/if}
        </span>
        <span class="decision" data-decision={r.decision}>{r.decision}</span>
      </div>
    {/snippet}
  </RowList>

  {#if viewing && records[selected]}
    <div class="preview" aria-label="MARC of the selected record">
      <p class="phead muted">
        {records[selected].title || "(untitled)"}
        <button class="button button--quiet mini" onclick={() => (viewing = false)}>Close</button>
      </p>
      <MarcRecordView record={records[selected].record} />
    </div>
  {/if}

  <p class="actions">
    {#if !readOnly}
      <button class="button" onclick={() => (confirming = true)} disabled={busy || !staged}>
        {staged ? `Commit batch (${importCount} import · ${records.length - importCount} skip)` : `Committed ${batch.committed} / skipped ${batch.skipped}`}
      </button>
    {/if}
    {#if committed && !readOnly}
      <button class="button button--quiet" onclick={() => (confirmingRevert = true)} disabled={busy}>Revert commit…</button>
    {/if}
    {#if batch.status === "REVERTED"}
      <span class="muted">reverted {batch.reverted} grain{batch.reverted === 1 ? "" : "s"}</span>
    {/if}
    {#if revertNote}<span class="ok" role="status">{revertNote}</span>{/if}
  </p>
  {#if revertSkips.length > 0}
    <details class="skips">
      <summary>{revertSkips.length} grain{revertSkips.length === 1 ? "" : "s"} skipped (kept as-is)</summary>
      <ul>
        {#each revertSkips as s (s.path)}
          <li><span class="mono">{s.path}</span> -- {s.reason}</li>
        {/each}
      </ul>
    </details>
  {/if}
</div>

{#if confirming}
  <Modal ariaLabel="Commit this batch" onclose={() => (confirming = false)} width="28rem">
    <h3>Commit "{batch.label}"?</h3>
    <p>
      {importCount} record{importCount === 1 ? "" : "s"} import, {records.length - importCount} skip, policy
      <strong>{batch.policy}</strong>. Committed records enter the catalog through the ingest pipeline.
    </p>
    <p class="confirm-actions">
      <button class="button button--quiet" onclick={() => (confirming = false)}>Cancel</button>
      <button class="button" data-autofocus onclick={() => void commit()} disabled={busy}>Commit batch</button>
    </p>
  </Modal>
{/if}

{#if confirmingRevert}
  <Modal ariaLabel="Revert this batch" onclose={() => (confirmingRevert = false)} width="28rem">
    <h3>Revert "{batch.label}"?</h3>
    <p>
      Overlaid grains return to their pre-commit bytes and works this batch created are tombstoned. Grains edited
      since the commit are left untouched and reported.
    </p>
    <p class="confirm-actions">
      <button class="button button--quiet" onclick={() => (confirmingRevert = false)}>Cancel</button>
      <button class="button" data-autofocus onclick={() => void revert()} disabled={busy}>Revert batch</button>
    </p>
  </Modal>
{/if}

<style>
  .review {
    border: 1px solid var(--rule);
    border-radius: 8px;
    padding: 0.7rem 1rem 1rem;
  }
  .rhead {
    display: flex;
    align-items: baseline;
    gap: 0.7rem;
  }
  .rhead h3 {
    margin: 0.1rem 0;
    flex: 1;
  }
  .row {
    display: flex;
    gap: 0.5rem;
    align-items: center;
    flex-wrap: wrap;
    margin: 0.4rem 0;
  }
  .mini {
    font-size: 0.72rem;
    padding: 0.05em 0.6em;
  }
  .rrow {
    display: grid;
    grid-template-columns: 2rem minmax(12rem, 1.4fr) auto minmax(0, auto) 4.5rem;
    gap: 0 0.55rem;
    align-items: baseline;
    padding: 0.2rem 0.55rem;
  }
  .rrow.tint-ok {
    background: var(--tint-ok);
  }
  .rrow.tint-danger {
    background: var(--tint-danger);
  }
  .mono {
    font-family: var(--mono);
    font-size: var(--fs-meta);
    color: var(--ink-muted);
  }
  .idx {
    text-align: right;
  }
  .title {
    font-weight: 600;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .match {
    font-size: 0.72rem;
    border-radius: 999px;
    padding: 0.05em 0.6em;
    border: 1px solid var(--rule);
    white-space: nowrap;
  }
  .match[data-kind="new"] {
    background: var(--surface);
  }
  .match[data-kind="work"] {
    border-color: #c77d0a;
  }
  .match[data-kind="instance"] {
    border-color: var(--danger);
  }
  .link {
    font-size: var(--fs-meta);
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .decision {
    font-size: var(--fs-meta);
    font-weight: 650;
    text-align: right;
  }
  .decision[data-decision="import"] {
    color: var(--ok);
  }
  .decision[data-decision="skip"] {
    color: var(--danger);
  }
  .preview {
    border: 1px solid var(--rule);
    border-radius: 8px;
    padding: 0.4rem 0.7rem 0.6rem;
    margin: 0.5rem 0;
    max-height: 50vh;
    overflow-y: auto;
  }
  .phead {
    display: flex;
    gap: 0.6rem;
    align-items: baseline;
    justify-content: space-between;
    font-size: 0.8rem;
    margin: 0.1rem 0 0.4rem;
  }
  .actions {
    margin-top: 0.8rem;
    display: flex;
    gap: 0.6rem;
    align-items: center;
    flex-wrap: wrap;
  }
  .ok {
    color: var(--accent);
    font-size: 0.85rem;
  }
  .skips {
    font-size: 0.85rem;
    margin-top: 0.3rem;
  }
  .skips ul {
    margin: 0.3rem 0;
    padding-left: 1.1rem;
  }
  .confirm-actions {
    display: flex;
    gap: 0.6rem;
    justify-content: flex-end;
  }
</style>
