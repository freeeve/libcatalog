<script lang="ts">
  // Moderation queue: filterable suggestion list with keyboard
  // triage. j/k move the selection; a/r/t stage approve, reject, and
  // reject+tombstone locally; s opens the vocabulary picker to approve with a
  // substitute term. The publish bar ships staged decisions as one
  // POST /v1/review batch. Folk-scheme rows add immediate accept/block
  // governance for librarians.
  import { onMount } from "svelte";
  import { ApiError, fetchQueue, humanApiMessage, postPublish, postReview, setFolkTermStatus } from "../lib/api";
  import { canPublish } from "../lib/auth";
  import { getConfig } from "../lib/config";
  import { createDecisionStore } from "../lib/decisions";
  import { bindKeys, popScope, pushScope } from "../lib/keyboard";
  import { navigate } from "../lib/router";
  import { screenState } from "../lib/screenState.svelte";
  import { sessionStore } from "../lib/stores";
  import { bestLabel } from "../lib/vocab";
  import PublishBar from "../components/PublishBar.svelte";
  import RowList from "../components/RowList.svelte";
  import VocabPicker from "../components/VocabPicker.svelte";
  import { SUGG_TYPES } from "../lib/types";
  import type { Decision, Suggestion, Term } from "../lib/types";

  const SCOPE = "queue";
  const STATUSES = ["PENDING", "APPROVED", "REJECTED", "DISPUTED"];
  const PROVENANCES = ["PATRON", "PIPELINE", "LIBRARIAN"];
  // Derived from the type union's own array so the filter can never again omit a
  // type the queue renders -- CONCERN was missing here while the union had it
  //.
  const TYPES = SUGG_TYPES;

  // Staged decisions mirror to sessionStorage so a reload or a drill-in to
  // a work mid-triage loses nothing.
  const decisions = createDecisionStore("lcat.queue.decisions.v1");
  const schemes = getConfig().schemes ?? [];
  const FRESH_MS = 60_000;

  // A deep link may pin the provenance filter (an enrichment job's "review
  // suggestions" lands on its PIPELINE output); the persisted screen state
  // otherwise stands.
  let { initialProvenance = "" }: { initialProvenance?: string } = $props();

  const st = screenState("queue", () => ({
    status: "PENDING",
    scheme: "",
    provenance: "",
    type: "",
    items: [] as Suggestion[],
    cursor: "",
    selected: 0,
    loadedAt: 0,
  }));
  // svelte-ignore state_referenced_locally -- the deep link pins the filter once, at mount
  if (initialProvenance && PROVENANCES.includes(initialProvenance)) {
    // svelte-ignore state_referenced_locally
    st.provenance = initialProvenance;
    st.loadedAt = 0; // force a fresh fetch under the pinned filter
  }

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
      u: { description: "unstage the selected row's decision", legend: "unstage", handler: unstageSelected },
      o: { description: "open the selected row's work", legend: "open work", handler: openSelected },
      Enter: { description: "open the selected row's work", hidden: true, handler: openSelected },
    });
    if (Date.now() - st.loadedAt > FRESH_MS) void load(true);
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
        status: st.status,
        scheme: st.scheme || undefined,
        provenance: st.provenance || undefined,
        type: st.type || undefined,
        cursor: reset ? undefined : st.cursor || undefined,
      });
      st.items = reset ? (page.items ?? []) : [...st.items, ...(page.items ?? [])];
      st.cursor = page.cursor ?? "";
      st.loadedAt = Date.now();
      st.selected = reset ? 0 : Math.min(st.selected, Math.max(0, st.items.length - 1));
    } catch (e) {
      error = e instanceof ApiError && e.status === 401 ? "session expired -- sign in again" : "queue load failed";
      if (reset) st.items = [];
    } finally {
      loading = false;
    }
  }

  function refilter(): void {
    st.cursor = "";
    void load(true);
  }

  function openSelected(): void {
    const s = st.items[st.selected];
    if (s) navigate(`/works/${encodeURIComponent(s.workId)}`);
  }

  function unstageSelected(): void {
    const s = st.items[st.selected];
    if (s) decisions.unstage(s.workId, s.term, s.type);
  }

  type Action = "approve" | "reject" | "tombstone" | "substitute";

  function act(action: Action, item?: Suggestion): void {
    const s = item ?? st.items[st.selected];
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
      // Decisions another moderator resolved first were discarded. Say so, and
      // keep them staged against their new status rather than clearing the
      // moderator's work away with nothing on screen to contradict the notice
      //.
      const stale = res.staleDecisions ?? [];
      if (stale.length > 0) {
        parts.push(`${stale.length} already decided by someone else`);
      }
      notice = parts.join(" · ");
      decisions.clear();
      for (const d of stale) decisions.stage(d);
      await load(true);
    } catch (e) {
      error = e instanceof ApiError ? `apply failed: ${humanApiMessage(e, "the request was rejected")}` : "apply failed";
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
      error = e instanceof ApiError ? `publish failed: ${humanApiMessage(e, "the request was rejected")}` : "publish failed";
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
      error = e instanceof ApiError ? `folk update failed: ${humanApiMessage(e, "the request was rejected")}` : "folk update failed";
    }
  }
</script>

<main class="queue wide" id="main" tabindex="-1">
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
      <select bind:value={st.status} onchange={refilter}>
        {#each STATUSES as s (s)}<option value={s}>{s}</option>{/each}
      </select>
    </label>
    <label>
      Scheme
      <select bind:value={st.scheme} onchange={refilter}>
        <option value="">any</option>
        {#each schemes as s (s)}<option value={s}>{s}</option>{/each}
      </select>
    </label>
    <label>
      Provenance
      <select bind:value={st.provenance} onchange={refilter}>
        <option value="">any</option>
        {#each PROVENANCES as p (p)}<option value={p}>{p}</option>{/each}
      </select>
    </label>
    <label>
      Type
      <select bind:value={st.type} onchange={refilter}>
        <option value="">any</option>
        {#each TYPES as t (t)}<option value={t}>{t}</option>{/each}
      </select>
    </label>
  </form>

  <p class="muted count" aria-live="polite">
    {#if loading && st.items.length === 0}
      Loading…
    {:else if error}
      <span class="error">{error}</span>
    {:else}
      {st.items.length} suggestion{st.items.length === 1 ? "" : "s"}{st.cursor ? " (more available)" : ""}
    {/if}
  </p>
  {#if notice}<p class="notice" role="status">{notice}</p>{/if}

  <RowList
    items={st.items}
    bind:selected={st.selected}
    getKey={(s) => s.workId + " " + s.term.scheme + " " + s.term.id + " " + s.type}
    ariaLabel="Suggestions"
    scope={SCOPE}
    itemName="suggestion"
    empty={!loading && !error ? "Nothing in the queue for these filters." : undefined}
  >
    {#snippet row(s: Suggestion, i: number, sel: boolean)}
      {@const staged = stagedFor($decisions, s)}
      <div
        class="qrow"
        class:tint-ok={staged?.approve}
        class:tint-danger={staged && !staged.approve}
      >
        <span class="supporters" title="{s.supporterCount} supporter{s.supporterCount === 1 ? '' : 's'}">{s.supporterCount}</span>
        <span class="chip chip--{s.type === 'ADD' ? 'add' : s.type === 'CONCERN' ? 'concern' : 'remove'}">{s.type}</span>
        <a class="work" href={"#/works/" + encodeURIComponent(s.workId)}>{s.workTitle || s.workId}</a>
        {#if s.type === "CONCERN"}
          <!-- A concern is freetext, not a term: show the note;
               approve reads as resolve, reject as dismiss. -->
          <span class="term concern-note" title={s.note}>{s.note}</span>
        {:else}
          <span class="term">
            {s.term.label || s.term.id}
            <span class="chip chip--scheme">{s.term.scheme}</span>
          </span>
        {/if}
        <span class="meta muted">
          {#if s.type === "REMOVE" && s.reasonCounts}<span class="reasons">{reasonSummary(s)}</span>{/if}
          <span class="chip chip--prov">
            {s.provenance}{s.provenance === "PIPELINE" && s.confidence !== undefined
              ? ` ${s.confidence.toFixed(2)}`
              : ""}
          </span>
        </span>
        <span class="staged">{#if staged}{stagedLabel(staged)}{/if}</span>
        {#if sel}
          <div class="acts">
            {#if s.type === "CONCERN"}
              <button class="button button--quiet" onclick={() => act("approve", s)}>Resolve</button>
              <button class="button button--quiet" onclick={() => act("reject", s)}>Dismiss</button>
            {:else}
              <button class="button button--quiet" onclick={() => act("approve", s)}>Approve</button>
              <button class="button button--quiet" onclick={() => act("reject", s)}>Reject</button>
              <button class="button button--quiet" onclick={() => act("tombstone", s)}>Tombstone</button>
              <button class="button button--quiet" onclick={() => act("substitute", s)}>Substitute…</button>
            {/if}
            {#if librarian && s.term.scheme === "folk"}
              <button class="button button--quiet" onclick={() => folk("acceptFolk", s)}>Accept folk</button>
              <button class="button button--quiet" onclick={() => folk("blockFolk", s)}>Block folk</button>
            {/if}
            <span class="muted when">{new Date(s.createdAt).toLocaleDateString()}</span>
          </div>
        {/if}
      </div>
    {/snippet}
  </RowList>

  {#if st.cursor}
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
  .filters select {
    min-height: var(--control-h-sm);
    font-size: var(--fs-row);
  }
  .count {
    margin: 0.35rem 0;
    font-size: var(--fs-meta);
  }
  .notice {
    color: var(--ok);
    font-weight: 600;
  }
  .qrow {
    display: grid;
    grid-template-columns: 1.6rem auto fit-content(26rem) minmax(9rem, 1fr) auto minmax(4rem, auto);
    gap: 0 0.55rem;
    align-items: baseline;
    padding: 0.2rem 0.55rem;
  }
  .qrow.tint-ok {
    background: var(--tint-ok);
  }
  .qrow.tint-danger {
    background: var(--tint-danger);
  }
  .supporters {
    font-family: var(--mono);
    font-size: var(--fs-meta);
    color: var(--ink-muted);
    text-align: right;
  }
  .work {
    font-weight: 600;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .term {
    font-weight: 600;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .meta {
    font-size: var(--fs-meta);
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
    text-align: right;
  }
  .staged {
    font-size: var(--fs-meta);
    font-weight: 650;
    color: var(--pend-ink);
    text-align: right;
    white-space: nowrap;
  }
  .acts {
    grid-column: 1 / -1;
    display: flex;
    gap: 0.4rem;
    flex-wrap: wrap;
    align-items: baseline;
    padding: 0.3rem 0 0.2rem;
  }
  .acts .button {
    font-size: 0.78rem;
    padding: 0.15em 0.7em;
    min-height: var(--control-h-sm);
  }
  .when {
    margin-left: auto;
    font-size: var(--fs-meta);
  }
  .chip {
    display: inline-block;
    font-size: 0.68rem;
    font-weight: 600;
    letter-spacing: 0.03em;
    padding: 0.05em 0.5em;
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
  .reasons {
    color: var(--danger);
  }
  .chip--concern {
    background: color-mix(in oklab, var(--accent) 18%, var(--surface));
  }
  .concern-note {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    font-style: italic;
  }
</style>
