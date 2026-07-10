<script lang="ts">
  // Local-authority editor: the SKOS description behind one heading, edited
  // as language-tagged rows (prefLabel/altLabel/definition) and URI lists
  // (broader/narrower/related/exactMatch), saved under If-Match. Field labels
  // and help come from the authority-topic profile -- the same profile
  // mechanism records use. The merge tool retires this term into a winner
  // picked from any loaded vocabulary and rewrites every referencing work
  // (tasks/046).
  import { onMount } from "svelte";
  import {
    fetchAuthority,
    fetchAuthorityProfile,
    updateAuthority,
    mergeAuthority,
    ApiError,
    ConflictError,
  } from "../lib/api";
  import { isReadOnly } from "../lib/config";
  import { bestLabel } from "../lib/vocab";
  import VocabPicker from "../components/VocabPicker.svelte";
  import type { AuthorityTerm, Profile, Term } from "../lib/types";

  let { authorityId }: { authorityId: string } = $props();

  const readOnly = isReadOnly();

  interface LangRow {
    lang: string;
    value: string;
  }

  let etag = $state("");
  let uri = $state("");
  let mergedInto = $state("");
  let profile = $state<Profile | null>(null);
  let prefRows = $state<LangRow[]>([]);
  let altRows = $state<LangRow[]>([]);
  let defRows = $state<LangRow[]>([]);
  let relations = $state<Record<string, string[]>>({ broader: [], narrower: [], related: [], exactMatch: [] });
  let uriDrafts = $state<Record<string, string>>({ broader: "", narrower: "", related: "", exactMatch: "" });
  let pickerFor = $state<string | null>(null);
  let merging = $state(false);
  let mergeWinner = $state<Term | null>(null);
  let loading = $state(true);
  let saving = $state(false);
  let status = $state("");
  let error = $state("");
  // A save conflict pauses instead of discarding: the cataloger's rows stay
  // as typed and they choose to overwrite or reload (tasks/114).
  let conflicted = $state(false);

  const relationPaths = ["broader", "narrower", "related", "exactMatch"];

  onMount(() => {
    void load();
    fetchAuthorityProfile().then(
      (p) => (profile = p),
      () => {},
    );
  });

  async function load(): Promise<void> {
    loading = true;
    error = "";
    try {
      const view = await fetchAuthority(authorityId);
      etag = view.etag;
      uri = view.term.uri ?? "";
      mergedInto = view.term.mergedInto ?? "";
      prefRows = toRows(view.term.prefLabel);
      altRows = toAltRows(view.term.altLabel);
      defRows = toRows(view.term.definition);
      relations = {
        broader: view.term.broader ?? [],
        narrower: view.term.narrower ?? [],
        related: view.term.related ?? [],
        exactMatch: view.term.exactMatch ?? [],
      };
    } catch (e) {
      error = e instanceof ApiError && e.status === 404 ? "no such authority" : "load failed";
    } finally {
      loading = false;
    }
  }

  function toRows(m?: Record<string, string>): LangRow[] {
    const rows = Object.entries(m ?? {}).map(([lang, value]) => ({ lang, value }));
    return rows.length > 0 ? rows : [{ lang: "en", value: "" }];
  }

  function toAltRows(m?: Record<string, string[]>): LangRow[] {
    const rows: LangRow[] = [];
    for (const [lang, values] of Object.entries(m ?? {})) {
      for (const value of values) rows.push({ lang, value });
    }
    return rows;
  }

  function assemble(): AuthorityTerm {
    const prefLabel: Record<string, string> = {};
    for (const r of prefRows) if (r.value.trim()) prefLabel[r.lang.trim()] = r.value.trim();
    const altLabel: Record<string, string[]> = {};
    for (const r of altRows) {
      if (!r.value.trim()) continue;
      (altLabel[r.lang.trim()] ??= []).push(r.value.trim());
    }
    const definition: Record<string, string> = {};
    for (const r of defRows) if (r.value.trim()) definition[r.lang.trim()] = r.value.trim();
    return {
      prefLabel,
      altLabel,
      definition,
      broader: relations.broader,
      narrower: relations.narrower,
      related: relations.related,
      exactMatch: relations.exactMatch,
    };
  }

  async function save(): Promise<void> {
    saving = true;
    status = "";
    error = "";
    try {
      const res = await updateAuthority(authorityId, assemble(), etag);
      etag = res.etag;
      conflicted = false;
      status = "saved -- the relabel is live in search and propagates at projection";
    } catch (e) {
      if (e instanceof ConflictError) {
        conflicted = true;
        error = "this term changed in another session while you edited";
      } else {
        error = e instanceof ApiError ? e.message : "save failed";
      }
    } finally {
      saving = false;
    }
  }

  /** Conflict resolution: keep the typed rows and retry the save over the
   *  fresh etag (overwriting the other session's change). */
  async function overwriteConflict(): Promise<void> {
    try {
      const view = await fetchAuthority(authorityId);
      etag = view.etag;
    } catch {
      error = "could not refresh the term -- try again";
      return;
    }
    conflicted = false;
    await save();
  }

  /** Conflict resolution: discard the typed rows and reload the fresh term. */
  async function discardConflict(): Promise<void> {
    conflicted = false;
    error = "";
    await load();
  }

  function addURI(path: string, value: string): void {
    const v = value.trim();
    if (!v || relations[path].includes(v)) return;
    relations = { ...relations, [path]: [...relations[path], v] };
    uriDrafts = { ...uriDrafts, [path]: "" };
  }

  function removeURI(path: string, value: string): void {
    relations = { ...relations, [path]: relations[path].filter((u) => u !== value) };
  }

  async function runMerge(): Promise<void> {
    if (!mergeWinner) return;
    merging = true;
    error = "";
    try {
      const result = await mergeAuthority(authorityId, {
        scheme: mergeWinner.scheme,
        id: mergeWinner.id,
        label: bestLabel(mergeWinner),
      });
      status = `merged -- ${result.rewritten} work${result.rewritten === 1 ? "" : "s"} rewritten to the winner`;
      mergeWinner = null;
      await load();
    } catch (e) {
      error = e instanceof ApiError ? e.message : "merge failed";
    } finally {
      merging = false;
    }
  }

  function fieldLabel(path: string, fallback: string): string {
    return profile?.fields.find((f) => f.path === path)?.label ?? fallback;
  }

  function fieldHelp(path: string): string {
    return profile?.fields.find((f) => f.path === path)?.help ?? "";
  }

  const heading = $derived(prefRows.find((r) => r.lang === "en" && r.value)?.value || prefRows[0]?.value || authorityId);
</script>

<main id="main" tabindex="-1">
  {#if loading}
    <p class="muted">Loading…</p>
  {:else if error && !etag}
    <p class="error">{error}</p>
  {:else}
    <h1>{heading}</h1>
    <p class="id">{uri}</p>

    {#if mergedInto}
      <p class="banner" role="status">
        This term is retired: merged into <span class="id">{mergedInto}</span>. Works were rewritten to the winner;
        edits here only affect the historical record.
      </p>
    {/if}

    <p aria-live="polite">
      {#if status}<span class="ok">{status}</span>{/if}
      {#if error}<span class="error">{error}</span>{/if}
    </p>

    <section aria-label={fieldLabel("prefLabel", "Preferred label")}>
      <h2>{fieldLabel("prefLabel", "Preferred label")}</h2>
      {#each prefRows as row, i (i)}
        <div class="row">
          <input class="lang" aria-label="Language" bind:value={row.lang} placeholder="lang" />
          <input class="val" aria-label="Label" bind:value={row.value} placeholder="Preferred label" />
          <button class="button button--quiet" onclick={() => (prefRows = prefRows.filter((_, j) => j !== i))}>Remove</button>
        </div>
      {/each}
      <button class="button button--quiet" onclick={() => (prefRows = [...prefRows, { lang: "", value: "" }])}>
        Add language
      </button>
    </section>

    <section aria-label={fieldLabel("altLabel", "Used for (variants)")}>
      <h2>{fieldLabel("altLabel", "Used for (variants)")}</h2>
      {#each altRows as row, i (i)}
        <div class="row">
          <input class="lang" aria-label="Language" bind:value={row.lang} placeholder="lang" />
          <input class="val" aria-label="Variant label" bind:value={row.value} placeholder="Variant (see-from) label" />
          <button class="button button--quiet" onclick={() => (altRows = altRows.filter((_, j) => j !== i))}>Remove</button>
        </div>
      {/each}
      <button class="button button--quiet" onclick={() => (altRows = [...altRows, { lang: "en", value: "" }])}>
        Add variant
      </button>
    </section>

    <section aria-label={fieldLabel("definition", "Scope note")}>
      <h2>{fieldLabel("definition", "Scope note")}</h2>
      {#each defRows as row, i (i)}
        <div class="row">
          <input class="lang" aria-label="Language" bind:value={row.lang} placeholder="lang" />
          <input class="val" aria-label="Scope note" bind:value={row.value} placeholder="Scope note" />
          <button class="button button--quiet" onclick={() => (defRows = defRows.filter((_, j) => j !== i))}>Remove</button>
        </div>
      {/each}
      <button class="button button--quiet" onclick={() => (defRows = [...defRows, { lang: "en", value: "" }])}>
        Add note
      </button>
    </section>

    {#each relationPaths as path (path)}
      <section aria-label={fieldLabel(path, path)}>
        <h2>{fieldLabel(path, path)}</h2>
        {#if fieldHelp(path)}
          <p class="muted help">{fieldHelp(path)}</p>
        {/if}
        <ul class="uris">
          {#each relations[path] as u (u)}
            <li>
              <span class="id">{u}</span>
              <button class="button button--quiet" onclick={() => removeURI(path, u)}>Remove</button>
            </li>
          {/each}
        </ul>
        <div class="row">
          <input
            class="val"
            aria-label={"Add " + fieldLabel(path, path) + " URI"}
            bind:value={uriDrafts[path]}
            placeholder="https://…"
            onkeydown={(ev) => {
              if (ev.key === "Enter") {
                ev.preventDefault();
                addURI(path, uriDrafts[path]);
              }
            }}
          />
          <button class="button button--quiet" onclick={() => addURI(path, uriDrafts[path])}>Add URI</button>
          <button class="button button--quiet" onclick={() => (pickerFor = path)}>Pick a term…</button>
        </div>
      </section>
    {/each}

    {#if !readOnly}
      {#if conflicted}
        <div class="merge-confirm" role="alertdialog" aria-label="Edit conflict">
          <p>
            Another session changed this term while you edited. Your typed changes are still here --
            save them over the other change, or discard them and reload the fresh term.
          </p>
          <p>
            <button class="button" onclick={() => void overwriteConflict()} disabled={saving}>Save mine anyway</button>
            <button class="button button--quiet" onclick={() => void discardConflict()} disabled={saving}>
              Discard mine and reload
            </button>
          </p>
        </div>
      {/if}
      <p class="actions">
        <button class="button" onclick={() => void save()} disabled={saving || conflicted}>{saving ? "Saving…" : "Save"}</button>
        {#if !mergedInto}
          <button class="button button--quiet" onclick={() => (pickerFor = "merge")}>Merge into another term…</button>
        {/if}
      </p>
    {/if}

    {#if mergeWinner}
      <div class="merge-confirm" role="alertdialog" aria-label="Confirm merge">
        <p>
          Merge <strong>{heading}</strong> into <strong>{bestLabel(mergeWinner)}</strong>
          <span class="id">({mergeWinner.scheme}: {mergeWinner.id})</span>?
          This retires the local term and rewrites every work that references it. It cannot be undone from here.
        </p>
        <p>
          <button class="button" onclick={() => void runMerge()} disabled={merging}>
            {merging ? "Merging…" : "Merge"}
          </button>
          <button class="button button--quiet" onclick={() => (mergeWinner = null)}>Cancel</button>
        </p>
      </div>
    {/if}
  {/if}
</main>

{#if pickerFor}
  <VocabPicker
    title={pickerFor === "merge" ? "Merge into…" : "Pick a " + fieldLabel(pickerFor, pickerFor) + " term"}
    onselect={(t) => {
      if (pickerFor === "merge") mergeWinner = t;
      else if (pickerFor) addURI(pickerFor, t.id);
      pickerFor = null;
    }}
    onclose={() => (pickerFor = null)}
  />
{/if}

<style>
  .id {
    font-family: var(--mono);
    font-size: 0.78rem;
    color: var(--ink-muted);
    word-break: break-all;
  }
  .banner {
    border: 1px solid var(--rule);
    border-left: 4px solid var(--accent);
    border-radius: 4px;
    background: var(--surface);
    padding: 0.6rem 0.9rem;
  }
  section {
    margin: 1.1rem 0;
  }
  h2 {
    font-size: 1rem;
    margin: 0 0 0.35rem;
  }
  .help {
    margin: 0 0 0.4rem;
    font-size: 0.85rem;
  }
  .row {
    display: flex;
    gap: 0.5rem;
    margin-bottom: 0.4rem;
    align-items: center;
  }
  .lang {
    width: 4.5rem;
  }
  .val {
    flex: 1;
    max-width: 32rem;
  }
  .uris {
    list-style: none;
    padding: 0;
    margin: 0 0 0.4rem;
  }
  .uris li {
    display: flex;
    gap: 0.6rem;
    align-items: center;
    padding: 0.15rem 0;
  }
  .actions {
    margin-top: 1.4rem;
    display: flex;
    gap: 0.75rem;
  }
  .merge-confirm {
    border: 1px solid var(--accent);
    border-radius: 6px;
    padding: 0.75rem 1rem;
    background: var(--surface);
    max-width: 40rem;
  }
  .ok {
    color: var(--accent);
  }
</style>
