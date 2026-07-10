<script lang="ts">
  // Admin surface for editing profiles (the JSON that replaces MARC
  // frameworks). The set is shipped as validated defaults; a deployment can
  // override any of them at runtime here. Editing is raw JSON on purpose --
  // the server runs the same Validate the framework test does, so a bad
  // profile is rejected on save and never reaches a cataloger's editor.
  import { onMount } from "svelte";
  import {
    ApiError,
    deleteProfileOverride,
    fetchProfile,
    fetchProfiles,
    putProfile,
  } from "../lib/api";
  import type { ProfileSummary } from "../lib/types";
  import { isReadOnly } from "../lib/config";
  import { setLeaveGuard } from "../lib/router";

  const readOnly = isReadOnly();

  let list = $state<ProfileSummary[]>([]);
  let selected = $state("");
  let text = $state("");
  let etag = $state("");
  let isDefault = $state(true);
  let dirty = $state(false);
  let busy = $state(false);
  let error = $state("");
  let status = $state("");

  onMount(() => {
    void loadList();
  });

  // Unsaved-JSON guard (tasks/206), mirroring the work editor's tasks/199
  // wiring: while dirty, in-app navigation asks and beforeunload arms the
  // browser's native prompt. Unlike work edits, no draft autosave backs
  // this screen -- a silent discard of hand-edited profile JSON is
  // unrecoverable.
  $effect(() => {
    if (!dirty) return;
    const unregister = setLeaveGuard(() => confirm("Discard unsaved changes to this profile?"));
    const onBeforeUnload = (ev: BeforeUnloadEvent) => ev.preventDefault();
    window.addEventListener("beforeunload", onBeforeUnload);
    return () => {
      unregister();
      window.removeEventListener("beforeunload", onBeforeUnload);
    };
  });

  async function loadList(): Promise<void> {
    try {
      const res = await fetchProfiles();
      list = Object.values(res.profiles ?? {}).sort((a, b) => a.id.localeCompare(b.id));
    } catch (e) {
      error = e instanceof ApiError ? e.message : "loading profiles failed";
    }
  }

  async function select(id: string): Promise<void> {
    if (dirty && !confirm("Discard unsaved changes?")) return;
    error = "";
    status = "";
    await loadProfile(id);
  }

  // loadProfile fetches one profile into the editor without the unsaved-changes
  // guard or clearing status, so callers (select, revert) control those.
  async function loadProfile(id: string): Promise<void> {
    try {
      const res = await fetchProfile(id);
      selected = id;
      text = JSON.stringify(res.profile, null, 2);
      etag = res.etag;
      isDefault = res.isDefault;
      dirty = false;
    } catch (e) {
      error = e instanceof ApiError ? e.message : "loading the profile failed";
    }
  }

  async function save(): Promise<void> {
    error = "";
    status = "";
    let parsed: unknown;
    try {
      parsed = JSON.parse(text);
    } catch {
      error = "not valid JSON";
      return;
    }
    busy = true;
    try {
      const res = await putProfile(selected, parsed, etag);
      etag = res.etag;
      isDefault = false;
      dirty = false;
      status = "saved";
      await loadList();
    } catch (e) {
      error = e instanceof ApiError ? e.message : "save failed";
    } finally {
      busy = false;
    }
  }

  async function revert(): Promise<void> {
    if (!confirm(`Revert ${selected} to the shipped default? This discards the override.`)) return;
    busy = true;
    error = "";
    status = "";
    try {
      await deleteProfileOverride(selected);
      dirty = false;
      await loadList();
      if (list.some((p) => p.id === selected)) {
        await loadProfile(selected); // reloads the now-default; keeps status
        status = "reverted to default";
      } else {
        // No shipped default behind the override: the DELETE removed the
        // profile outright, so clear the editor instead of 404ing into a
        // stale selection (tasks/206).
        selected = "";
        text = "";
        etag = "";
        isDefault = true;
        status = "override deleted -- this profile had no shipped default";
      }
    } catch (e) {
      error = e instanceof ApiError ? e.message : "revert failed";
    } finally {
      busy = false;
    }
  }
</script>

<main class="wide" id="main" tabindex="-1">
  <h1>Editing profiles</h1>
  <p class="muted">
    Runtime overrides for the field definitions the editor and op builder render
    from. Saves are validated server-side; overrides persist and revert to the
    shipped defaults.
  </p>

  {#if error}<p class="notice error" role="alert">{error}</p>{/if}
  {#if status}<p class="notice ok" aria-live="polite">{status}</p>{/if}

  <div class="cols">
    <ul class="plist" aria-label="Profiles">
      {#each list as p (p.id)}
        <li>
          <button class="row" class:current={p.id === selected} onclick={() => void select(p.id)}>
            <span class="pid">{p.id}</span>
            <span class="plabel muted">{p.label}</span>
            <span class="pmeta muted">{p.resourceType} · {p.fields.length} field{p.fields.length === 1 ? "" : "s"}</span>
            {#if p.overridden}<span class="badge">overridden</span>{/if}
          </button>
        </li>
      {/each}
    </ul>

    <section class="editor" aria-label="Profile editor">
      {#if selected}
        <div class="ehead">
          <strong>{selected}</strong>
          <span class="muted">{isDefault ? "shipped default" : "overridden"}</span>
          <span class="spacer"></span>
          {#if !isDefault && !readOnly}
            <button class="button button--quiet" onclick={() => void revert()} disabled={busy}>Revert to default</button>
          {/if}
          {#if !readOnly}
            <button class="button" onclick={() => void save()} disabled={busy || !dirty}>Save override</button>
          {/if}
        </div>
        <textarea
          class="json"
          spellcheck="false"
          aria-label="Profile JSON"
          readonly={readOnly}
          bind:value={text}
          oninput={() => {
            dirty = true;
            status = "";
          }}
        ></textarea>
      {:else}
        <p class="muted pick">Select a profile to edit.</p>
      {/if}
    </section>
  </div>
</main>

<style>
  .cols {
    display: grid;
    grid-template-columns: minmax(14rem, 22rem) 1fr;
    gap: 1rem;
    max-width: 72rem;
    align-items: start;
  }
  .plist {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: 0.3rem;
  }
  .row {
    display: grid;
    gap: 0.1rem;
    width: 100%;
    text-align: left;
    background: var(--bg);
    border: 1px solid var(--rule);
    border-radius: 8px;
    padding: 0.5rem 0.7rem;
    color: inherit;
    cursor: pointer;
    font: inherit;
  }
  .row:hover {
    border-color: var(--accent);
  }
  .row.current {
    box-shadow: inset 3px 0 0 var(--accent);
    border-color: var(--accent);
  }
  .pid {
    font-family: var(--mono);
    font-weight: 650;
    font-size: 0.9rem;
  }
  .plabel {
    font-size: 0.85rem;
  }
  .pmeta {
    font-size: 0.75rem;
  }
  .badge {
    justify-self: start;
    font-size: 0.66rem;
    text-transform: uppercase;
    letter-spacing: 0.06em;
    color: var(--accent);
    border: 1px solid var(--accent);
    border-radius: 4px;
    padding: 0.02em 0.4em;
    margin-top: 0.15rem;
  }
  .editor {
    display: flex;
    flex-direction: column;
    gap: 0.5rem;
    min-width: 0;
  }
  .ehead {
    display: flex;
    align-items: center;
    gap: 0.6rem;
    flex-wrap: wrap;
  }
  .ehead .spacer {
    flex: 1;
  }
  .json {
    width: 100%;
    min-height: 28rem;
    resize: vertical;
    font-family: var(--mono);
    font-size: 0.82rem;
    line-height: 1.45;
    color: var(--ink);
    background: var(--bg);
    border: 1px solid var(--rule);
    border-radius: 8px;
    padding: 0.7rem 0.9rem;
    tab-size: 2;
  }
  .pick {
    padding: 2rem 0;
  }
  .notice {
    border-radius: 6px;
    padding: 0.5rem 0.8rem;
    max-width: 72rem;
  }
  .notice.error {
    border: 1px solid var(--danger, #c0392b);
    color: var(--danger, #c0392b);
  }
  .notice.ok {
    border: 1px solid var(--rule);
    color: var(--ink-muted);
  }
</style>
