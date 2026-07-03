<script lang="ts">
  // Duplicate-detection worklist (tasks/051): Works sharing a clustering key
  // (author+title+language) but holding separate ids. Expanding a pair loads
  // both editing docs side by side; picking a survivor merges through the
  // same endpoint (and therefore the same lcat:mergedInto semantics) as the
  // CLI markers. Field cleanup happens in the survivor's editor afterward.
  import { onMount } from "svelte";
  import { ApiError, fetchDuplicates, fetchWorkDoc, mergeWorks } from "../lib/api";
  import type { DuplicateGroup, WorkDoc } from "../lib/types";

  let groups = $state<DuplicateGroup[]>([]);
  let open = $state<string | null>(null);
  let docs = $state<Record<string, WorkDoc | null>>({});
  let survivor = $state("");
  let busy = $state(false);
  let status = $state("");
  let error = $state("");

  onMount(() => void load());

  async function load(): Promise<void> {
    busy = true;
    error = "";
    try {
      groups = (await fetchDuplicates()).groups ?? [];
    } catch (e) {
      error = e instanceof ApiError ? e.message : "loading duplicates failed";
    } finally {
      busy = false;
    }
  }

  async function expand(g: DuplicateGroup): Promise<void> {
    open = open === g.key ? null : g.key;
    survivor = g.works[0]?.workId ?? "";
    if (open === null) return;
    for (const w of g.works) {
      if (docs[w.workId] !== undefined) continue;
      try {
        docs = { ...docs, [w.workId]: (await fetchWorkDoc(w.workId)).doc };
      } catch {
        docs = { ...docs, [w.workId]: null };
      }
    }
  }

  /** Union of field paths across the group's docs, for the compare table. */
  function paths(g: DuplicateGroup): string[] {
    const seen = new Set<string>();
    for (const w of g.works) {
      for (const p of Object.keys(docs[w.workId]?.work.fields ?? {})) seen.add(p);
    }
    return [...seen].sort();
  }

  function values(workId: string, path: string): string {
    return (docs[workId]?.work.fields[path] ?? []).map((v) => v.v).join(" · ");
  }

  async function merge(g: DuplicateGroup): Promise<void> {
    if (!survivor) return;
    const losers = g.works.filter((w) => w.workId !== survivor);
    busy = true;
    error = "";
    try {
      for (const l of losers) {
        await mergeWorks(l.workId, survivor);
      }
      status = `merged ${losers.map((l) => l.workId).join(", ")} into ${survivor} -- the retired ids redirect after the next ingest`;
      open = null;
      await load();
    } catch (e) {
      error = e instanceof ApiError ? e.message : "merge failed";
    } finally {
      busy = false;
    }
  }
</script>

<main>
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

  <ul class="groups">
    {#each groups as g (g.key)}
      <li>
        <button class="ghead" onclick={() => void expand(g)} aria-expanded={open === g.key}>
          {g.works.map((w) => w.title || w.workId).join("  ·  ")}
          <span class="muted">({g.works.length} works)</span>
        </button>

        {#if open === g.key}
          <div class="compare">
            <table>
              <thead>
                <tr>
                  <th scope="col">Field</th>
                  {#each g.works as w (w.workId)}
                    <th scope="col">
                      <label class="pick">
                        <input type="radio" name={"surv-" + g.key} value={w.workId} bind:group={survivor} />
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
                      <td>{values(w.workId, p)}</td>
                    {/each}
                  </tr>
                {/each}
              </tbody>
            </table>
            <p class="acts">
              <button class="button" onclick={() => void merge(g)} disabled={busy || !survivor}>
                Merge into {survivor || "…"}
              </button>
              <span class="muted">then tidy fields in the survivor's editor</span>
            </p>
          </div>
        {/if}
      </li>
    {:else}
      <li class="muted">{busy ? "Scanning…" : "No duplicate candidates."}</li>
    {/each}
  </ul>
</main>

<style>
  .groups {
    list-style: none;
    padding: 0;
    margin: 0.6rem 0;
  }
  .groups li {
    border-bottom: 1px solid var(--rule);
    padding: 0.35rem 0;
  }
  .ghead {
    background: none;
    border: 0;
    color: inherit;
    font-weight: 600;
    text-align: left;
    width: 100%;
    padding: 0.25rem 0;
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
  .pick {
    display: inline-flex;
    gap: 0.35rem;
    align-items: center;
    font-weight: 600;
  }
  .acts {
    display: flex;
    gap: 0.7rem;
    align-items: center;
    margin-top: 0.6rem;
  }
  .ok {
    color: var(--accent);
  }
</style>
