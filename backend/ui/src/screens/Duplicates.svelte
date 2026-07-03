<script lang="ts">
  // Duplicate-detection worklist (tasks/051): Works sharing a clustering key
  // (author+title+language) but holding separate ids. The group list is a
  // keyboard list (scope "duplicates", Enter/o expands); an expanded group
  // pushes the "duplicates-compare" scope where 1-9 pick the survivor by
  // column, m merges behind a confirm modal, and Escape collapses. Merging
  // uses the same endpoint (and lcat:mergedInto semantics) as the CLI
  // markers; field cleanup happens in the survivor's editor afterward.
  import { onDestroy, onMount } from "svelte";
  import { ApiError, fetchDuplicates, fetchWorkDoc, mergeWorks } from "../lib/api";
  import { bindKeys, popScope, pushScope } from "../lib/keyboard";
  import { screenState } from "../lib/screenState.svelte";
  import Modal from "../components/Modal.svelte";
  import RowList from "../components/RowList.svelte";
  import type { DuplicateGroup, WorkDoc } from "../lib/types";

  const SCOPE = "duplicates";
  const COMPARE_SCOPE = "duplicates-compare";
  const FRESH_MS = 60_000;

  const st = screenState("duplicates", () => ({
    groups: [] as DuplicateGroup[],
    selected: 0,
    openKey: null as string | null,
    survivor: "",
    docs: {} as Record<string, WorkDoc | null>,
    loadedAt: 0,
  }));

  let busy = $state(false);
  let confirming = $state(false);
  let status = $state("");
  let error = $state("");

  const openGroup = $derived(st.groups.find((g) => g.key === st.openKey) ?? null);

  pushScope(SCOPE);
  onDestroy(() => popScope(SCOPE));

  onMount(() => {
    const unbindList = bindKeys(SCOPE, {
      o: { description: "expand the selected group", hidden: true, handler: () => void toggleSelected() },
    });
    const unbindCompare = bindKeys(COMPARE_SCOPE, {
      m: { description: "merge the group into the survivor", legend: "merge", handler: () => openGroup && (confirming = true) },
      Escape: { description: "collapse the group", legend: "close", handler: collapse },
    });
    // The compare scope survives drill-in: re-push when returning with a
    // group still open.
    if (st.openKey) pushScope(COMPARE_SCOPE);
    if (Date.now() - st.loadedAt > FRESH_MS) void load();
    return () => {
      unbindList();
      unbindCompare();
      popScope(COMPARE_SCOPE);
    };
  });

  async function load(): Promise<void> {
    busy = true;
    error = "";
    try {
      st.groups = (await fetchDuplicates()).groups ?? [];
      st.loadedAt = Date.now();
      st.selected = Math.min(st.selected, Math.max(0, st.groups.length - 1));
      if (st.openKey && !st.groups.some((g) => g.key === st.openKey)) collapse();
    } catch (e) {
      error = e instanceof ApiError ? e.message : "loading duplicates failed";
    } finally {
      busy = false;
    }
  }

  function toggleSelected(): Promise<void> {
    const g = st.groups[st.selected];
    return g ? expand(g) : Promise.resolve();
  }

  async function expand(g: DuplicateGroup): Promise<void> {
    if (st.openKey === g.key) {
      collapse();
      return;
    }
    if (!st.openKey) pushScope(COMPARE_SCOPE);
    st.openKey = g.key;
    st.survivor = g.works[0]?.workId ?? "";
    for (const w of g.works) {
      if (st.docs[w.workId] !== undefined) continue;
      try {
        st.docs = { ...st.docs, [w.workId]: (await fetchWorkDoc(w.workId)).doc };
      } catch {
        st.docs = { ...st.docs, [w.workId]: null };
      }
    }
  }

  function collapse(): void {
    if (st.openKey) popScope(COMPARE_SCOPE);
    st.openKey = null;
    confirming = false;
  }

  /** 1-9 pick the survivor by column ordinal in the open group. */
  function pickByOrdinal(n: number): void {
    const w = openGroup?.works[n - 1];
    if (w) st.survivor = w.workId;
  }

  onMount(() => {
    const specs: Parameters<typeof bindKeys>[1] = {};
    for (let n = 1; n <= 9; n++) {
      specs[String(n)] = {
        description: `keep work ${n} as the survivor`,
        legend: "pick survivor",
        keyLabel: "1-9",
        hidden: n > 1,
        handler: () => pickByOrdinal(n),
      };
    }
    return bindKeys(COMPARE_SCOPE, specs);
  });

  /** Union of field paths across the group's docs, for the compare table. */
  function paths(g: DuplicateGroup): string[] {
    const seen = new Set<string>();
    for (const w of g.works) {
      for (const p of Object.keys(st.docs[w.workId]?.work.fields ?? {})) seen.add(p);
    }
    return [...seen].sort();
  }

  function values(workId: string, path: string): string {
    return (st.docs[workId]?.work.fields[path] ?? []).map((v) => v.v).join(" · ");
  }

  async function merge(g: DuplicateGroup): Promise<void> {
    if (!st.survivor) return;
    confirming = false;
    const losers = g.works.filter((w) => w.workId !== st.survivor);
    busy = true;
    error = "";
    try {
      for (const l of losers) {
        await mergeWorks(l.workId, st.survivor);
      }
      status = `merged ${losers.map((l) => l.workId).join(", ")} into ${st.survivor} -- the retired ids redirect after the next ingest`;
      collapse();
      await load();
    } catch (e) {
      error = e instanceof ApiError ? e.message : "merge failed";
    } finally {
      busy = false;
    }
  }
</script>

<main class="wide">
  <h1>Duplicates</h1>
  <p class="muted">
    Works sharing a clustering key but minted separately. Merging records the decision on the survivor; the retired
    Work's editions move across on the next ingest and its URL redirects.
  </p>
  <p aria-live="polite">
    {#if busy}<span class="muted">Working…</span>{/if}
    {#if status}<span class="ok">{status}</span>{/if}
    {#if error}<span class="error">{error}</span>{/if}
  </p>

  <RowList
    items={st.groups}
    bind:selected={st.selected}
    getKey={(g) => g.key}
    ariaLabel="Duplicate groups"
    scope={SCOPE}
    itemName="group"
    onactivate={(g) => void expand(g)}
    empty={busy ? "Scanning…" : "No duplicate candidates."}
  >
    {#snippet row(g: DuplicateGroup)}
      <div class="grow-wrap">
        <button class="ghead" onclick={() => void expand(g)} aria-expanded={st.openKey === g.key}>
          {g.works.map((w) => w.title || w.workId).join("  ·  ")}
          <span class="muted">({g.works.length} works)</span>
        </button>

        {#if st.openKey === g.key}
          <div class="compare">
            <table>
              <thead>
                <tr>
                  <th scope="col">Field</th>
                  {#each g.works as w, wi (w.workId)}
                    <th scope="col">
                      <label class="pick">
                        {#if wi < 9}<kbd class="ord">{wi + 1}</kbd>{/if}
                        <input type="radio" name={"surv-" + g.key} value={w.workId} bind:group={st.survivor} />
                        keep <a href={"#/works/" + encodeURIComponent(w.workId)}>{w.workId}</a>
                      </label>
                    </th>
                  {/each}
                </tr>
              </thead>
              <tbody>
                {#each paths(g) as p (p)}
                  <tr>
                    <th scope="row">{p}</th>
                    {#each g.works as w (w.workId)}
                      <td class:survivor={w.workId === st.survivor}>{values(w.workId, p)}</td>
                    {/each}
                  </tr>
                {/each}
              </tbody>
            </table>
            <p class="acts">
              <button class="button" onclick={() => (confirming = true)} disabled={busy || !st.survivor}>
                Merge into {st.survivor || "…"}
              </button>
              <span class="muted">then tidy fields in the survivor's editor</span>
            </p>
          </div>
        {/if}
      </div>
    {/snippet}
  </RowList>
</main>

{#if confirming && openGroup}
  <Modal ariaLabel="Confirm merge" onclose={() => (confirming = false)} width="30rem">
    <h3>Merge into {st.survivor}?</h3>
    <p>
      {openGroup.works.length - 1} work{openGroup.works.length === 2 ? "" : "s"} retire and redirect:
      <code>{openGroup.works.filter((w) => w.workId !== st.survivor).map((w) => w.workId).join(", ")}</code>
    </p>
    <p class="confirm-acts">
      <button class="button button--quiet" onclick={() => (confirming = false)}>Cancel</button>
      <button class="button" data-autofocus onclick={() => openGroup && void merge(openGroup)} disabled={busy}>
        Merge into {st.survivor}
      </button>
    </p>
  </Modal>
{/if}

<style>
  .grow-wrap {
    padding: 0.15rem 0.55rem 0.2rem;
  }
  .ghead {
    background: none;
    border: 0;
    color: inherit;
    font-weight: 600;
    text-align: left;
    width: 100%;
    padding: 0.12rem 0;
    font-size: var(--fs-row);
  }
  .compare {
    border: 1px solid var(--rule);
    border-radius: 8px;
    padding: 0.6rem 0.9rem;
    margin: 0.4rem 0 0.6rem;
    overflow-x: auto;
  }
  table {
    border-collapse: collapse;
    width: 100%;
    font-size: 0.88rem;
  }
  th,
  td {
    text-align: left;
    padding: 0.3rem 0.6rem;
    border-bottom: 1px solid var(--rule);
    vertical-align: top;
  }
  td.survivor {
    background: var(--tint-ok);
  }
  .pick {
    display: inline-flex;
    gap: 0.35rem;
    align-items: center;
    font-weight: 600;
  }
  .ord {
    font-family: var(--mono);
    font-size: 0.72rem;
    background: var(--surface);
    border: 1px solid var(--rule);
    border-bottom-width: 2px;
    border-radius: 4px;
    padding: 0 0.35em;
  }
  .acts {
    display: flex;
    gap: 0.7rem;
    align-items: center;
    margin-top: 0.6rem;
  }
  .confirm-acts {
    display: flex;
    gap: 0.6rem;
    justify-content: flex-end;
  }
  .ok {
    color: var(--accent);
  }
</style>
