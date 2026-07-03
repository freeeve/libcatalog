<script lang="ts">
  // Moderation queue (task 044): filterable suggestion list with keyboard
  // triage. j/k move the selection; a/r/t stage approve, reject, and
  // reject+tombstone locally; s opens the vocabulary picker to approve with a
  // substitute term. The publish bar ships staged decisions as one
  // POST /v1/review batch. Folk-scheme rows add immediate accept/block
  // governance for librarians.
  import { onMount } from "svelte";
  import { ApiError, fetchQueue, postPublish, postReview, setFolkTermStatus } from "../lib/api";
  import { canPublish } from "../lib/auth";
  import { getConfig } from "../lib/config";
  import { createDecisionStore } from "../lib/decisions";
  import { bindKeys, popScope, pushScope } from "../lib/keyboard";
  import { sessionStore } from "../lib/stores";
  import { bestLabel } from "../lib/vocab";
  import PublishBar from "../components/PublishBar.svelte";
  import RowList from "../components/RowList.svelte";
  import VocabPicker from "../components/VocabPicker.svelte";
  import type { Decision, Suggestion, Term } from "../lib/types";

  const SCOPE = "queue";
  const STATUSES = ["PENDING", "APPROVED", "REJECTED", "DISPUTED"];
  const PROVENANCES = ["PATRON", "PIPELINE", "LIBRARIAN"];
  const TYPES = ["ADD", "REMOVE"];

  const decisions = createDecisionStore();
  const schemes = getConfig().schemes ?? [];

  let status = $state("PENDING");
  let scheme = $state("");
  let provenance = $state("");
  let type = $state("");
  let items = $state<Suggestion[]>([]);
  let cursor = $state("");
  let selected = $state(0);
  let loading = $state(false);
  let applying = $state(false);
  let error = $state("");
  let notice = $state("");
  let pickerFor = $state<Suggestion | null>(null);

  const librarian = $derived(canPublish($sessionStore));
  const approveCount = $derived($decisions.filter((d) => d.approve).length);
  const rejectCount = $derived($decisions.length - approveCount);

  onMount(() => {
    pushScope(SCOPE);
    const unbind = bindKeys(SCOPE, {
      a: { description: "stage approve for the selected row", legend: "approve", handler: () => act("approve") },
      r: { description: "stage reject for the selected row", legend: "reject", handler: () => act("reject") },
      t: { description: "stage reject + tombstone for the selected row", legend: "tombstone", handler: () => act("tombstone") },
      s: { description: "approve with a substitute term", legend: "substitute", handler: () => act("substitute") },
    });
    void load(true);
    return () => {
      unbind();
      popScope(SCOPE);
    };
  });

  async function load(reset: boolean): Promise<void> {
    loading = true;
    error = "";
    try {
      const page = await fetchQueue({
        status,
        scheme: scheme || undefined,
        provenance: provenance || undefined,
        type: type || undefined,
        cursor: reset ? undefined : cursor || undefined,
      });
      items = reset ? (page.items ?? []) : [...items, ...(page.items ?? [])];
      cursor = page.cursor ?? "";
      if (reset) selected = 0;
      else selected = Math.min(selected, Math.max(0, items.length - 1));
    } catch (e) {
      error = e instanceof ApiError && e.status === 401 ? "session expired -- sign in again" : "queue load failed";
      if (reset) items = [];
    } finally {
      loading = false;
    }
  }

  function refilter(): void {
    cursor = "";
    void load(true);
  }

  type Action = "approve" | "reject" | "tombstone" | "substitute";

  function act(action: Action, item?: Suggestion): void {
    const s = item ?? items[selected];
    if (!s) return;
    if (action === "substitute") {
      pickerFor = s;
      return;
    }
    decisions.stage({
      workId: s.workId,
      term: s.term,
      type: s.type,
      approve: action === "approve",
      ...(action === "tombstone" ? { tombstone: true } : {}),
    });
  }

  function substituteChosen(term: Term): void {
    const s = pickerFor;
    pickerFor = null;
    if (!s) return;
    decisions.stage({
      workId: s.workId,
      term: s.term,
      type: s.type,
      approve: true,
      substituteTerm: { scheme: term.scheme, id: term.id, label: bestLabel(term) },
    });
  }

  function stagedFor(list: Decision[], s: Suggestion): Decision | undefined {
    return list.find(
      (d) => d.workId === s.workId && d.term.scheme === s.term.scheme && d.term.id === s.term.id && d.type === s.type,
    );
  }

  function stagedLabel(d: Decision): string {
    if (!d.approve) return d.tombstone ? "reject + tombstone" : "reject";
    return d.substituteTerm ? `approve → ${d.substituteTerm.label || d.substituteTerm.id}` : "approve";
  }

  function reasonSummary(s: Suggestion): string {
    return Object.entries(s.reasonCounts ?? {})
      .sort(([, a], [, b]) => b - a)
      .map(([reason, n]) => `${reason} ×${n}`)
      .join(", ");
  }

  async function apply(publish: boolean): Promise<void> {
    if ($decisions.length === 0) return;
    applying = true;
    notice = "";
    error = "";
    try {
      const res = await postReview(decisions.payload(), publish);
      const parts = [`reviewed ${res.reviewed}`];
      if (res.published !== undefined) parts.push(`published ${res.published}`);
      if (res.skipped) parts.push(`skipped ${res.skipped}`);
      if (res.publishNote) parts.push(res.publishNote);
      notice = parts.join(" · ");
      decisions.clear();
      await load(true);
    } catch (e) {
      error = e instanceof ApiError ? `apply failed: ${e.message}` : "apply failed";
    } finally {
      applying = false;
    }
  }

  async function publishApproved(): Promise<void> {
    applying = true;
    notice = "";
    error = "";
    try {
      const res = await postPublish();
      const parts = [`published ${res.published ?? 0}`];
      if (res.skipped) parts.push(`skipped ${res.skipped}`);
      if (res.publishNote) parts.push(res.publishNote);
      notice = parts.join(" · ");
    } catch (e) {
      error = e instanceof ApiError ? `publish failed: ${e.message}` : "publish failed";
    } finally {
      applying = false;
    }
  }

  async function folk(action: "acceptFolk" | "blockFolk", s: Suggestion): Promise<void> {
    notice = "";
    error = "";
    try {
      await setFolkTermStatus(action, s.term.id);
      notice = `${action === "acceptFolk" ? "accepted" : "blocked"} folk term "${s.term.label || s.term.id}"`;
    } catch (e) {
      error = e instanceof ApiError ? `folk update failed: ${e.message}` : "folk update failed";
    }
  }
</script>

<main class="queue">
  <header class="qhead">
    <h1>Review queue</h1>
    <a href="#/promotions">Tag promotions</a>
    {#if librarian}
      <button class="button button--quiet" onclick={publishApproved} disabled={applying}>Publish approved</button>
    {/if}
  </header>

  <form class="filters" aria-label="Queue filters" onsubmit={(ev) => ev.preventDefault()}>
    <label>
      Status
      <select bind:value={status} onchange={refilter}>
        {#each STATUSES as s (s)}<option value={s}>{s}</option>{/each}
      </select>
    </label>
    <label>
      Scheme
      <select bind:value={scheme} onchange={refilter}>
        <option value="">any</option>
        {#each schemes as s (s)}<option value={s}>{s}</option>{/each}
      </select>
    </label>
    <label>
      Provenance
      <select bind:value={provenance} onchange={refilter}>
        <option value="">any</option>
        {#each PROVENANCES as p (p)}<option value={p}>{p}</option>{/each}
      </select>
    </label>
    <label>
      Type
      <select bind:value={type} onchange={refilter}>
        <option value="">any</option>
        {#each TYPES as t (t)}<option value={t}>{t}</option>{/each}
      </select>
    </label>
  </form>

  <p class="muted" aria-live="polite">
    {#if loading && items.length === 0}
      Loading…
    {:else if error}
      <span class="error">{error}</span>
    {:else}
      {items.length} suggestion{items.length === 1 ? "" : "s"}{cursor ? " (more available)" : ""}
    {/if}
  </p>
  {#if notice}<p class="notice" role="status">{notice}</p>{/if}

  <RowList
    items={items}
    bind:selected
    getKey={(s) => s.workId + " " + s.term.scheme + " " + s.term.id + " " + s.type}
    ariaLabel="Suggestions"
    scope={SCOPE}
    itemName="suggestion"
    empty={!loading && !error ? "Nothing in the queue for these filters." : undefined}
  >
    {#snippet row(s: Suggestion)}
      {@const staged = stagedFor($decisions, s)}
      <div class="qrow">
        <div class="what">
          <a class="work" href={"#/works/" + encodeURIComponent(s.workId)}>{s.workTitle || s.workId}</a>
          <span class="chip chip--{s.type === 'ADD' ? 'add' : 'remove'}">{s.type}</span>
          <span class="term">{s.term.label || s.term.id}</span>
          <span class="chip chip--scheme">{s.term.scheme}</span>
          {#if staged}<span class="chip chip--staged">{stagedLabel(staged)}</span>{/if}
        </div>
        <div class="meta muted">
          <span>{s.supporterCount} supporter{s.supporterCount === 1 ? "" : "s"}</span>
          <span class="chip chip--prov">
            {s.provenance}{s.provenance === "PIPELINE" && s.confidence !== undefined
              ? ` ${s.confidence.toFixed(2)}`
              : ""}
          </span>
          {#if s.type === "REMOVE" && s.reasonCounts}
            <span class="reasons">{reasonSummary(s)}</span>
          {/if}
          <span>{new Date(s.createdAt).toLocaleDateString()}</span>
        </div>
        <div class="acts">
          <button class="button button--quiet" onclick={() => act("approve", s)}>Approve</button>
          <button class="button button--quiet" onclick={() => act("reject", s)}>Reject</button>
          <button class="button button--quiet" onclick={() => act("tombstone", s)}>Tombstone</button>
          <button class="button button--quiet" onclick={() => act("substitute", s)}>Substitute…</button>
          {#if librarian && s.term.scheme === "folk"}
            <button class="button button--quiet" onclick={() => folk("acceptFolk", s)}>Accept folk</button>
            <button class="button button--quiet" onclick={() => folk("blockFolk", s)}>Block folk</button>
          {/if}
        </div>
      </div>
    {/snippet}
  </RowList>

  {#if cursor}
    <p><button class="button button--quiet" onclick={() => void load(false)} disabled={loading}>Load more</button></p>
  {/if}

  <PublishBar {approveCount} {rejectCount} canPublishNow={librarian} busy={applying} onapply={(p) => void apply(p)} onclear={() => decisions.clear()} />
</main>

{#if pickerFor}
  <VocabPicker
    title={`Substitute term for "${pickerFor.term.label || pickerFor.term.id}"`}
    onselect={substituteChosen}
    onclose={() => (pickerFor = null)}
  />
{/if}

<style>
  .qhead {
    display: flex;
    align-items: baseline;
    gap: 1rem;
  }
  .qhead h1 {
    flex: 1;
    margin-bottom: 0.4rem;
  }
  .filters {
    display: flex;
    gap: 1rem;
    flex-wrap: wrap;
    align-items: end;
    padding: 0.5rem 0;
    border-bottom: 1px solid var(--rule);
  }
  .filters label {
    display: grid;
    gap: 0.15rem;
    font-size: 0.8rem;
    font-weight: 600;
    color: var(--ink-muted);
  }
  .notice {
    color: var(--ok);
    font-weight: 600;
  }
  .qrow {
    padding: 0.5rem 0.6rem;
  }
  .what {
    display: flex;
    align-items: baseline;
    gap: 0.55rem;
    flex-wrap: wrap;
  }
  .work {
    font-weight: 600;
  }
  .term {
    font-weight: 600;
  }
  .meta {
    display: flex;
    gap: 0.9rem;
    flex-wrap: wrap;
    font-size: 0.85rem;
    margin: 0.2rem 0 0.35rem;
  }
  .acts {
    display: flex;
    gap: 0.4rem;
    flex-wrap: wrap;
  }
  .acts .button {
    font-size: 0.8rem;
    padding: 0.25em 0.8em;
  }
  .chip {
    display: inline-block;
    font-size: 0.72rem;
    font-weight: 600;
    letter-spacing: 0.03em;
    padding: 0.1em 0.55em;
    border-radius: 999px;
    border: 1px solid transparent;
    white-space: nowrap;
  }
  .chip--add {
    background: #e2f2e7;
    color: #175a2e;
    border-color: #b5dcc2;
  }
  .chip--remove {
    background: #fbe9e9;
    color: #8a2020;
    border-color: #eec5c5;
  }
  .chip--scheme {
    background: #eceef1;
    color: #444a52;
    border-color: #d4d8dd;
    font-family: var(--mono);
  }
  .chip--prov {
    background: #e3edf9;
    color: #1c4f8a;
    border-color: #bcd3ef;
  }
  .chip--staged {
    background: var(--pend-bg);
    color: var(--pend-ink);
    border-color: var(--pend-edge);
  }
  .reasons {
    color: var(--danger);
  }
</style>
