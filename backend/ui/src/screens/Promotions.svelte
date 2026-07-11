<script lang="ts">
  // Tag promotions (task 044): moderators propose folding a community tag
  // into a controlled term; librarians decide, and approval executes the
  // batch rewrite (the response reports how many works it touched).
  //
  // Approval runs the rewrite first and only then stamps APPROVED,
  // so a failed rewrite leaves the row PENDING with its Approve button live and
  // the count of what it managed to rewrite recorded. Decided rows carry a
  // Delete, which is the only way out of an approval made with no publisher.
  import { onMount } from "svelte";
  import { ApiError, decidePromotion, deletePromotion, fetchPromotions, humanApiMessage, proposePromotion } from "../lib/api";
  import { bindKeys, popScope, pushScope } from "../lib/keyboard";
  import { canPublish } from "../lib/auth";
  import { sessionStore } from "../lib/stores";
  import { bestLabel } from "../lib/vocab";
  import TagInput from "../components/TagInput.svelte";
  import VocabPicker from "../components/VocabPicker.svelte";
  import type { Promotion, Term, TermRef } from "../lib/types";

  const SCOPE = "promotions";

  let promotions = $state<Promotion[]>([]);
  let selected = $state(0);
  let loading = $state(false);
  let error = $state("");
  let notice = $state("");

  let tag = $state("");
  let chosen = $state<TermRef | null>(null);
  let picking = $state(false);
  let submitting = $state(false);
  let formError = $state("");
  let formKey = $state(0); // bumping it remounts TagInput to clear the text

  const librarian = $derived(canPublish($sessionStore));
  const pending = $derived(promotions.filter((p) => p.status === "PENDING"));
  const decided = $derived(promotions.filter((p) => p.status !== "PENDING"));

  onMount(() => {
    pushScope(SCOPE);
    const unbind = bindKeys(SCOPE, {
      j: { description: "next pending proposal", legend: "move", keyLabel: "j/k", handler: () => move(1) },
      k: { description: "previous pending proposal", hidden: true, handler: () => move(-1) },
      ArrowDown: { description: "next pending proposal", hidden: true, handler: () => move(1) },
      ArrowUp: { description: "previous pending proposal", hidden: true, handler: () => move(-1) },
      a: { description: "approve the selected proposal", legend: "approve", handler: () => decideSelected(true) },
      r: { description: "reject the selected proposal", legend: "reject", handler: () => decideSelected(false) },
    });
    void load();
    return () => {
      unbind();
      popScope(SCOPE);
    };
  });

  function move(delta: number): void {
    if (pending.length === 0) return;
    selected = Math.min(pending.length - 1, Math.max(0, selected + delta));
  }

  function decideSelected(approve: boolean): void {
    const p = pending[selected];
    if (p && librarian) void decide(p, approve);
  }

  async function load(): Promise<void> {
    loading = true;
    error = "";
    try {
      const res = await fetchPromotions();
      promotions = res.promotions ?? [];
    } catch (e) {
      error = e instanceof ApiError && e.status === 401 ? "session expired -- sign in again" : "promotions load failed";
    } finally {
      loading = false;
    }
  }

  function termChosen(t: Term): void {
    picking = false;
    chosen = { scheme: t.scheme, id: t.id, label: bestLabel(t) };
  }

  async function propose(ev: SubmitEvent): Promise<void> {
    ev.preventDefault();
    const trimmed = tag.trim();
    if (!trimmed || !chosen) return;
    submitting = true;
    formError = "";
    notice = "";
    try {
      await proposePromotion(trimmed, { scheme: chosen.scheme, id: chosen.id });
      notice = `proposed "${trimmed}" → ${chosen.label || chosen.id}`;
      tag = "";
      chosen = null;
      formKey += 1;
      await load();
    } catch (e) {
      formError =
        e instanceof ApiError && e.status === 409
          ? "this tag already has an open proposal"
          : e instanceof ApiError
            ? `propose failed: ${humanApiMessage(e, "the request was rejected")}`
            : "propose failed";
    } finally {
      submitting = false;
    }
  }

  async function decide(p: Promotion, approve: boolean): Promise<void> {
    notice = "";
    error = "";
    try {
      const res = await decidePromotion(p.tag, approve);
      notice = approve
        ? `approved "${p.tag}" -- rewrote ${res.works} work${res.works === 1 ? "" : "s"}${res.note ? ` (${res.note})` : ""}`
        : `rejected "${p.tag}"`;
      await load();
    } catch (e) {
      error = e instanceof ApiError ? `decide failed: ${humanApiMessage(e, "the request was rejected")}` : "decide failed";
      // A failed approval leaves the row PENDING with its partial count updated,
      // so reload rather than leave stale numbers on screen.
      if (approve) await load();
    }
  }

  async function remove(p: Promotion): Promise<void> {
    notice = "";
    error = "";
    try {
      await deletePromotion(p.tag);
      notice = `deleted the promotion for "${p.tag}" -- the tag can be proposed again`;
      await load();
    } catch (e) {
      error = e instanceof ApiError ? `delete failed: ${humanApiMessage(e, "the request was rejected")}` : "delete failed";
    }
  }

  function when(iso?: string): string {
    return iso ? new Date(iso).toLocaleDateString() : "";
  }
</script>

<main id="main" tabindex="-1">
  <header class="phead">
    <h1>Tag promotions</h1>
    <a href="#/queue">Review queue</a>
  </header>
  <p class="muted">Fold a community tag into a controlled vocabulary term across every carrying work.</p>

  <section aria-label="Propose a promotion">
    <h2>Propose</h2>
    <form class="propose" onsubmit={propose}>
      {#key formKey}
        <TagInput id="promo-tag" label="Community tag" onselect={(t) => (tag = t)} oninput={(t) => (tag = t)} />
      {/key}
      <div class="termrow">
        <span class="muted">Target term:</span>
        {#if chosen}
          <strong>{chosen.label || chosen.id}</strong>
          <span class="chip">{chosen.scheme}</span>
        {:else}
          <span class="muted">none chosen</span>
        {/if}
        <button type="button" class="button button--quiet" onclick={() => (picking = true)}>Choose term…</button>
      </div>
      {#if formError}<p class="error" role="alert">{formError}</p>{/if}
      <button class="button" type="submit" disabled={submitting || !tag.trim() || !chosen}>
        {submitting ? "Proposing…" : "Propose promotion"}
      </button>
    </form>
  </section>

  <p class="muted" aria-live="polite">
    {#if loading}Loading…{:else if error}<span class="error">{error}</span>{/if}
  </p>
  {#if notice}<p class="notice" role="status">{notice}</p>{/if}

  <section aria-label="Pending promotions">
    <h2>Pending</h2>
    {#if pending.length === 0}
      <p class="muted">No pending proposals.</p>
    {:else}
      <ul class="promos">
        {#each pending as p, i (p.tag)}
          <li class:selected={i === selected} onfocusin={() => (selected = i)}>
            <div class="what">
              <strong class="tag">{p.tag}</strong>
              <span aria-hidden="true">→</span>
              <span>{p.term.label || p.term.id}</span>
              <span class="chip">{p.term.scheme}</span>
            </div>
            <div class="meta muted">
              <span>proposed by {p.proposedBy}</span>
              <span>{when(p.createdAt)}</span>
              {#if (p.works ?? 0) > 0}
                <!-- An earlier approval failed partway. Approving again resumes:
                     the rewrite skips works that already lost the tag. -->
                <span>{p.works} work{p.works === 1 ? "" : "s"} already rewritten by a failed attempt</span>
              {/if}
            </div>
            {#if librarian}
              <div class="acts">
                <button class="button button--quiet" onclick={() => void decide(p, true)}>
                  {(p.works ?? 0) > 0 ? "Resume" : "Approve"}
                </button>
                <button class="button button--quiet" onclick={() => void decide(p, false)}>Reject</button>
              </div>
            {/if}
          </li>
        {/each}
      </ul>
    {/if}
  </section>

  <section aria-label="Decided promotions">
    <h2>Decided</h2>
    {#if decided.length === 0}
      <p class="muted">Nothing decided yet.</p>
    {:else}
      <ul class="promos">
        {#each decided as p (p.tag)}
          <li>
            <div class="what">
              <strong class="tag">{p.tag}</strong>
              <span aria-hidden="true">→</span>
              <span>{p.term.label || p.term.id}</span>
              <span class="chip">{p.term.scheme}</span>
              <span class="chip chip--{p.status === 'APPROVED' ? 'ok' : 'no'}">{p.status}</span>
            </div>
            <div class="meta muted">
              {#if p.decidedBy}<span>decided by {p.decidedBy}</span>{/if}
              {#if p.decidedAt}<span>{when(p.decidedAt)}</span>{/if}
              {#if p.status === "APPROVED"}<span>{p.works ?? 0} work{(p.works ?? 0) === 1 ? "" : "s"} rewritten</span>{/if}
            </div>
            {#if librarian}
              <!-- A decided row had no control of any kind. An approval made with
                   no publisher wired is executed nowhere and cannot be re-decided
                   or re-proposed; deleting it frees the tag. -->
              <div class="acts">
                <button class="button button--quiet" onclick={() => void remove(p)}>Delete</button>
              </div>
            {/if}
          </li>
        {/each}
      </ul>
    {/if}
  </section>
</main>

{#if picking}
  <VocabPicker title="Promotion target term" onselect={termChosen} onclose={() => (picking = false)} />
{/if}

<style>
  .phead {
    display: flex;
    align-items: baseline;
    gap: 1rem;
  }
  .phead h1 {
    flex: 1;
    margin-bottom: 0.2rem;
  }
  .propose {
    display: grid;
    gap: 0.6rem;
    justify-items: start;
    border: 1px solid var(--rule);
    border-radius: 8px;
    padding: 0.75rem 1rem 1rem;
  }
  .termrow {
    display: flex;
    align-items: baseline;
    gap: 0.6rem;
    flex-wrap: wrap;
  }
  .notice {
    color: var(--ok);
    font-weight: 600;
  }
  .promos {
    list-style: none;
    margin: 0.25rem 0;
    padding: 0;
  }
  .promos li {
    border-bottom: 1px solid var(--rule);
    padding: 0.5rem 0.2rem;
  }
  .promos li.selected {
    background: var(--surface);
    box-shadow: inset 3px 0 0 var(--accent);
  }
  .what {
    display: flex;
    align-items: baseline;
    gap: 0.55rem;
    flex-wrap: wrap;
  }
  .tag {
    font-family: var(--mono);
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
    background: #eceef1;
    color: #444a52;
    border: 1px solid #d4d8dd;
    white-space: nowrap;
  }
  .chip--ok {
    background: #e2f2e7;
    color: #175a2e;
    border-color: #b5dcc2;
  }
  .chip--no {
    background: #fbe9e9;
    color: #8a2020;
    border-color: #eec5c5;
  }
</style>
