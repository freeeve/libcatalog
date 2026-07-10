<script lang="ts">
  // Duplicate-detection worklist (tasks/051): Works sharing a clustering key
  // (author+title+language) but holding separate ids. The group list is a
  // keyboard list (scope "duplicates", Enter/o expands); an expanded group
  // pushes the "duplicates-compare" scope where 1-9 pick the survivor by
  // column, m merges behind a confirm modal, and Escape collapses. Merging
  // uses the same endpoint (and lcat:mergedInto semantics) as the CLI
  // markers; field cleanup happens in the survivor's editor afterward.
  import { onDestroy, onMount } from "svelte";
  import { ApiError, fetchDuplicates, fetchProfile, fetchWorkDoc, mergeWorks, postOps } from "../lib/api";
  import { bindKeys, popScope, pushScope } from "../lib/keyboard";
  import { screenState } from "../lib/screenState.svelte";
  import Modal from "../components/Modal.svelte";
  import RowList from "../components/RowList.svelte";
  import { isReadOnly } from "../lib/config";
  import { languageTerm } from "../lib/languages";
  import { rdaTerm } from "../lib/rdaterms";
  import { adoptionChanges, adoptionOps } from "../lib/mergeadopt";
  import type { DuplicateGroup, FieldValue, Op, Profile, WorkDoc } from "../lib/types";

  const SCOPE = "duplicates";
  const COMPARE_SCOPE = "duplicates-compare";
  const FRESH_MS = 60_000;
  const readOnly = isReadOnly();

  const st = screenState("duplicates", () => ({
    groups: [] as DuplicateGroup[],
    selected: 0,
    openKey: null as string | null,
    survivor: "",
    docs: {} as Record<string, WorkDoc | null>,
    etags: {} as Record<string, string>,
    // path -> the losing work whose values the survivor should take (058
    // item 6). One source per field: adopting twice replaces, so the table
    // can never stage two contradictory values for one path.
    adopted: {} as Record<string, string>,
    loadedAt: 0,
  }));

  let busy = $state(false);
  let confirming = $state(false);
  let status = $state("");
  let error = $state("");
  let profile = $state<Profile | null>(null);

  const openGroup = $derived(st.groups.find((g) => g.key === st.openKey) ?? null);

  pushScope(SCOPE);
  onDestroy(() => popScope(SCOPE));

  onMount(() => {
    const unbindList = bindKeys(SCOPE, {
      o: { description: "expand the selected group", hidden: true, handler: () => void toggleSelected() },
    });
    const unbindCompare = bindKeys(COMPARE_SCOPE, {
      m: { description: "merge the group into the survivor", legend: "merge", handler: () => !readOnly && openGroup && (confirming = true) },
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
    st.adopted = {};
    for (const w of g.works) {
      if (st.docs[w.workId] !== undefined) continue;
      try {
        const res = await fetchWorkDoc(w.workId);
        st.docs = { ...st.docs, [w.workId]: res.doc };
        st.etags = { ...st.etags, [w.workId]: res.etag };
      } catch {
        st.docs = { ...st.docs, [w.workId]: null };
      }
    }
    await loadProfile();
  }

  /** The survivor's profile, for each field's cardinality: max 1 means an
   *  adopted value replaces, otherwise it joins. Without it, adoption is
   *  offered on no field rather than guessed at. */
  async function loadProfile(): Promise<void> {
    const id = st.docs[st.survivor]?.profileId;
    if (!id || profile?.id === id) return;
    try {
      profile = (await fetchProfile(id)).profile;
    } catch {
      profile = null;
    }
  }

  function collapse(): void {
    if (st.openKey) popScope(COMPARE_SCOPE);
    st.openKey = null;
    st.adopted = {};
    confirming = false;
  }

  /** 1-9 pick the survivor by column ordinal in the open group. */
  function pickByOrdinal(n: number): void {
    const w = openGroup?.works[n - 1];
    if (w) setSurvivor(w.workId);
  }

  /** Changing the survivor drops the staged adoptions: they were expressed
   *  as "take that column's value", and the column they would land on has
   *  moved. Silently re-pointing them would stage edits nobody chose. */
  function setSurvivor(workId: string): void {
    if (workId === st.survivor) return;
    st.survivor = workId;
    st.adopted = {};
    void loadProfile();
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

  /** Display values: an IRI whose grain carries a name (the doc annotation,
   *  tasks/140) or that names a known closed-list term (RDA media/carrier,
   *  MARC language) shows the name; the raw value stays in the title tooltip. */
  function values(workId: string, path: string): { text: string; raw: string }[] {
    return (st.docs[workId]?.work.fields[path] ?? []).map((v) => ({
      text: (v.iri && (v.annotation ?? rdaTerm(v.v)?.label ?? languageTerm(v.v)?.label)) || v.v,
      raw: v.v,
    }));
  }

  /** One line per instance: the edition facts (ISBN, publisher, date,
   *  extent) that actually distinguish separately-minted records. */
  function instanceLines(workId: string): string[] {
    const doc = st.docs[workId];
    if (!doc) return [];
    return doc.instances.map((inst) => {
      const part = (path: string) => (inst.fields[path] ?? []).map((v) => v.v).join(", ");
      const facts = ["isbn", "publisher", "publicationDate", "extent"].map(part).filter(Boolean);
      return facts.length ? facts.join(" · ") : inst.id;
    });
  }

  /** The survivor's profile entry for a path, or undefined when the field is
   *  outside it (another profile's field, or a passthrough path). */
  function field(path: string) {
    return profile?.fields.find((f) => f.path === path);
  }

  /** Raw doc values for a work's field, the shape the adoption helpers take. */
  function fieldsOf(workId: string, path: string): FieldValue[] {
    return st.docs[workId]?.work.fields[path] ?? [];
  }

  /** A cell can be adopted when the field is in the survivor's profile, the
   *  column is not the survivor, and adopting it would actually change
   *  something. */
  function adoptable(path: string, workId: string): boolean {
    if (readOnly || workId === st.survivor || !st.survivor || !field(path)) return false;
    return adoptionChanges(fieldsOf(st.survivor, path), fieldsOf(workId, path), fieldMax(path));
  }

  function fieldMax(path: string): number | undefined {
    return field(path)?.max;
  }

  function toggleAdopt(path: string, workId: string): void {
    st.adopted = st.adopted[path] === workId ? omit(st.adopted, path) : { ...st.adopted, [path]: workId };
  }

  function omit(map: Record<string, string>, key: string): Record<string, string> {
    const { [key]: _drop, ...rest } = map;
    return rest;
  }

  function adoptOps(): Op[] {
    return adoptionOps(st.adopted, fieldsOf, st.survivor, fieldMax);
  }

  const stagedCount = $derived(Object.keys(st.adopted).length);

  async function merge(g: DuplicateGroup): Promise<void> {
    if (!st.survivor) return;
    confirming = false;
    const losers = g.works.filter((w) => w.workId !== st.survivor);
    busy = true;
    error = "";
    try {
      // Adoptions land on the survivor before the merge markers, so a failed
      // write leaves the group intact and re-mergeable rather than merged
      // without the fields the cataloger chose.
      const ops = adoptOps();
      // Count fields, not ops: a repeatable field adopts as one add per value.
      const fields = new Set(ops.map((o) => o.path)).size;
      if (ops.length > 0) {
        await postOps(st.survivor, ops, { ifMatch: st.etags[st.survivor] });
      }
      for (const l of losers) {
        await mergeWorks(l.workId, st.survivor);
      }
      const adopted = fields > 0 ? ` after adopting ${fields} field${fields === 1 ? "" : "s"}` : "";
      status = `merged ${losers.map((l) => l.workId).join(", ")} into ${st.survivor}${adopted} -- the retired ids redirect after the next ingest`;
      // The survivor's etag moved and the losers are retired: nothing cached
      // about this group is still true.
      st.docs = {};
      st.etags = {};
      collapse();
      await load();
    } catch (e) {
      error = e instanceof ApiError ? e.message : "merge failed";
    } finally {
      busy = false;
    }
  }
</script>

<main class="wide" id="main" tabindex="-1">
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
                        <input
                          type="radio"
                          name={"surv-" + g.key}
                          value={w.workId}
                          checked={st.survivor === w.workId}
                          onchange={() => setSurvivor(w.workId)}
                        />
                        keep <a href={"#/works/" + encodeURIComponent(w.workId)}>{w.title || w.workId}</a>
                        {#if w.title}<span class="wid">{w.workId}</span>{/if}
                      </label>
                    </th>
                  {/each}
                </tr>
              </thead>
              <tbody>
                {#if g.works.some((w) => (st.docs[w.workId]?.instances.length ?? 0) > 0)}
                  <tr>
                    <th scope="row">instances</th>
                    {#each g.works as w (w.workId)}
                      <td class:survivor={w.workId === st.survivor}>
                        {#each instanceLines(w.workId) as line, li (li)}
                          <span class="instline">{line}</span>
                        {:else}
                          <span class="muted">none</span>
                        {/each}
                      </td>
                    {/each}
                  </tr>
                {/if}
                {#each paths(g) as p (p)}
                  <tr>
                    <th scope="row">
                      {field(p)?.label ?? p}
                      {#if st.adopted[p]}<span class="staged" title="staged: this field will take the marked column's value">staged</span>{/if}
                    </th>
                    {#each g.works as w (w.workId)}
                      <td class:survivor={w.workId === st.survivor} class:taking={st.adopted[p] === w.workId}>
                        {#each values(w.workId, p) as val, vi (val.raw + vi)}
                          {#if vi > 0}<span class="sep" aria-hidden="true">·</span>{/if}
                          <span title={val.raw !== val.text ? val.raw : undefined}>{val.text}</span>
                        {:else}
                          <span class="muted">—</span>
                        {/each}
                        {#if adoptable(p, w.workId)}
                          <button
                            class="button button--quiet adopt"
                            aria-pressed={st.adopted[p] === w.workId}
                            title={fieldMax(p) === 1
                              ? `stage: the survivor's ${field(p)?.label ?? p} is replaced by this value`
                              : `stage: this value joins the survivor's ${field(p)?.label ?? p}`}
                            onclick={() => toggleAdopt(p, w.workId)}
                          >
                            {st.adopted[p] === w.workId ? "staged ✓" : "adopt"}
                          </button>
                        {/if}
                      </td>
                    {/each}
                  </tr>
                {:else}
                  <tr>
                    <td colspan={g.works.length + 1} class="muted">
                      Nothing to compare -- these works expose no editable fields (still loading, or not real catalog records).
                    </td>
                  </tr>
                {/each}
              </tbody>
            </table>
            {#if !readOnly}
              <p class="acts">
                <button class="button" onclick={() => (confirming = true)} disabled={busy || !st.survivor}>
                  Merge into {st.survivor || "…"}
                </button>
                <span class="muted">
                  {#if stagedCount > 0}
                    {stagedCount} field{stagedCount === 1 ? "" : "s"} staged onto the survivor
                  {:else}
                    adopt a losing record's field before merging, or tidy afterwards in the survivor's editor
                  {/if}
                </span>
              </p>
            {/if}
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
    {#if stagedCount > 0}
      <p>
        First, {stagedCount} field{stagedCount === 1 ? "" : "s"} adopted onto the survivor:
        <code>{Object.keys(st.adopted).map((p) => field(p)?.label ?? p).join(", ")}</code>
      </p>
    {/if}
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
  td.taking {
    outline: 2px solid var(--accent);
    outline-offset: -2px;
  }
  .adopt {
    margin-left: 0.4rem;
    font-size: var(--fs-meta);
  }
  .staged {
    margin-left: 0.4rem;
    font-size: var(--fs-meta);
    font-weight: 400;
    color: var(--accent);
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
  .wid {
    font-family: var(--mono);
    font-size: 0.72rem;
    font-weight: 400;
    color: var(--ink-muted);
  }
  .instline {
    display: block;
  }
  .sep {
    color: var(--ink-muted);
    padding: 0 0.25em;
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
