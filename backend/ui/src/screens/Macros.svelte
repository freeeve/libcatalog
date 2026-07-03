<script lang="ts">
  // Macro management (tasks/047): list own + shared macros, edit the
  // definition (label, shortcut key, sharing, parameters, the op list as
  // JSON), delete, or jump to the batch screen to run one over a selection.
  // Recording happens in the work editor ("Save staged edits as macro…");
  // this screen is where recordings get parameterized and shared.
  import { onMount } from "svelte";
  import { ApiError, createMacro, deleteMacro, fetchMacros, updateMacro } from "../lib/api";
  import { bindKeys, popScope, pushScope } from "../lib/keyboard";
  import { sessionStore } from "../lib/stores";
  import type { Macro, MacroParam, Op } from "../lib/types";

  const SCOPE = "macros";

  let macros = $state<Macro[]>([]);
  let selected = $state(0);
  let editing = $state<Macro | null>(null);
  let label = $state("");
  let keys = $state("");
  let shared = $state(false);
  let opsJSON = $state("");
  let params = $state<MacroParam[]>([]);
  let error = $state("");
  let status = $state("");

  const me = $derived($sessionStore?.email ?? "");

  onMount(() => {
    pushScope(SCOPE);
    const unbind = bindKeys(SCOPE, {
      j: { description: "next macro", legend: "move", keyLabel: "j/k", handler: () => move(1) },
      k: { description: "previous macro", hidden: true, handler: () => move(-1) },
      ArrowDown: { description: "next macro", hidden: true, handler: () => move(1) },
      ArrowUp: { description: "previous macro", hidden: true, handler: () => move(-1) },
      Enter: { description: "edit the selected macro", legend: "edit", handler: editSelected },
      n: { description: "start a new macro", legend: "new", handler: startNew },
    });
    void load();
    return () => {
      unbind();
      popScope(SCOPE);
    };
  });

  function move(delta: number): void {
    if (macros.length === 0) return;
    selected = Math.min(macros.length - 1, Math.max(0, selected + delta));
  }

  function editSelected(): void {
    const m = macros[selected];
    if (m && m.owner === me) startEdit(m);
  }

  async function load(): Promise<void> {
    try {
      macros = (await fetchMacros()).macros ?? [];
    } catch {
      error = "loading macros failed";
    }
  }

  function startNew(): void {
    editing = null;
    label = "";
    keys = "";
    shared = false;
    params = [];
    opsJSON = JSON.stringify(
      [{ resource: "work", path: "summary", action: "set", values: [{ v: "${text}", lang: "en" }] }],
      null,
      2,
    );
    status = "";
    error = "";
  }

  function startEdit(m: Macro): void {
    editing = m;
    label = m.label;
    keys = m.keys ?? "";
    shared = m.shared;
    params = structuredClone($state.snapshot(m.params) ?? []);
    opsJSON = JSON.stringify(m.ops, null, 2);
    status = "";
    error = "";
  }

  function parseOps(): Op[] | null {
    try {
      const parsed = JSON.parse(opsJSON) as Op[];
      if (!Array.isArray(parsed) || parsed.length === 0) throw new Error("empty");
      return parsed;
    } catch {
      error = "ops must be a non-empty JSON array of operations";
      return null;
    }
  }

  async function save(): Promise<void> {
    error = "";
    const ops = parseOps();
    if (!ops) return;
    const body = { label, keys: keys.trim(), shared, ops, params: params.filter((p) => p.name) };
    try {
      if (editing) {
        await updateMacro(editing.id, body);
        status = "macro updated";
      } else {
        await createMacro(body);
        status = "macro recorded";
      }
      editing = null;
      await load();
    } catch (e) {
      error = e instanceof ApiError ? e.message : "saving the macro failed";
    }
  }

  async function remove(m: Macro): Promise<void> {
    error = "";
    try {
      await deleteMacro(m.id);
      if (editing?.id === m.id) editing = null;
      await load();
      status = `deleted "${m.label}"`;
    } catch (e) {
      error = e instanceof ApiError && e.status === 403 ? "only the owner can delete a macro" : "delete failed";
    }
  }
</script>

<main>
  <h1>Macros</h1>
  <p class="muted">
    A macro is a replayable list of field edits: record one from staged edits in the work editor, replay it on another
    record, or share it and run it over a batch selection -- the modification-template workflow.
  </p>

  <p aria-live="polite">
    {#if status}<span class="ok">{status}</span>{/if}
    {#if error}<span class="error">{error}</span>{/if}
  </p>

  <div class="cols">
    <section aria-label="Macro list">
      <p><button class="button" onclick={startNew}>New macro</button></p>
      <ul class="list">
        {#each macros as m, i (m.id)}
          <li class:selected={i === selected} onfocusin={() => (selected = i)}>
            <span class="name">
              {m.label}
              {#if m.keys}<kbd>{m.keys}</kbd>{/if}
              {#if m.shared}<span class="badge">shared</span>{/if}
            </span>
            <span class="meta muted">{m.ops.length} op{m.ops.length === 1 ? "" : "s"} · {m.owner}</span>
            <span class="acts">
              <a class="button button--quiet" href={"#/batch?macro=" + encodeURIComponent(m.id)}>Run over selection…</a>
              {#if m.owner === me}
                <button class="button button--quiet" onclick={() => startEdit(m)}>Edit</button>
                <button class="button button--quiet" onclick={() => void remove(m)}>Delete</button>
              {/if}
            </span>
          </li>
        {:else}
          <li class="muted">No macros yet -- record one from the work editor or create one here.</li>
        {/each}
      </ul>
    </section>

    {#if editing !== null || label !== "" || opsJSON !== ""}
      <section aria-label="Macro editor" class="editor">
        <h2>{editing ? `Edit "${editing.label}"` : "New macro"}</h2>
        <div class="row">
          <label for="m-label">Label</label>
          <input id="m-label" class="grow" bind:value={label} placeholder="e.g. Stamp series summary" />
        </div>
        <div class="row">
          <label for="m-keys">Shortcut key</label>
          <input id="m-keys" class="keys" bind:value={keys} maxlength="1" placeholder="1" />
          <label class="check"><input type="checkbox" bind:checked={shared} /> Shared with the library</label>
        </div>

        <h3>Parameters</h3>
        {#each params as p, i (i)}
          <div class="row">
            <input aria-label="Parameter name" class="keys wide" bind:value={p.name} placeholder="name" />
            <input aria-label="Parameter label" class="grow" bind:value={p.label} placeholder="Prompt label" />
            <input aria-label="Parameter default" class="grow" bind:value={p.default} placeholder="Default (optional)" />
            <button class="button button--quiet" onclick={() => (params = params.filter((_, j) => j !== i))}>Remove</button>
          </div>
        {/each}
        <p>
          <button class="button button--quiet" onclick={() => (params = [...params, { name: "", label: "", default: "" }])}>
            Add parameter
          </button>
          <span class="muted">referenced in op values as <code>{"${name}"}</code></span>
        </p>

        <h3>Operations</h3>
        <textarea aria-label="Operations JSON" bind:value={opsJSON} rows="10" spellcheck="false"></textarea>

        <p class="actions">
          <button class="button" onclick={() => void save()}>{editing ? "Save changes" : "Create macro"}</button>
          {#if editing}
            <button class="button button--quiet" onclick={() => (editing = null)}>Cancel</button>
          {/if}
        </p>
      </section>
    {/if}
  </div>
</main>

<style>
  .cols {
    display: grid;
    grid-template-columns: minmax(20rem, 1fr) minmax(22rem, 1fr);
    gap: 1.5rem;
    align-items: start;
  }
  @media (max-width: 60rem) {
    .cols {
      grid-template-columns: 1fr;
    }
  }
  .list {
    list-style: none;
    margin: 0;
    padding: 0;
  }
  .list li {
    display: grid;
    gap: 0.15rem 0.8rem;
    padding: 0.5rem 0.4rem;
    border-bottom: 1px solid var(--rule);
  }
  .list li.selected {
    background: var(--surface);
    box-shadow: inset 3px 0 0 var(--accent);
  }
  .name {
    font-weight: 600;
  }
  kbd {
    font-family: var(--mono);
    font-size: 0.72rem;
    border: 1px solid var(--rule);
    border-radius: 4px;
    padding: 0 0.35em;
    margin-left: 0.4em;
  }
  .badge {
    font-size: 0.7rem;
    font-weight: 700;
    text-transform: uppercase;
    color: var(--accent-ink);
    background: var(--accent);
    border-radius: 999px;
    padding: 0.08em 0.6em;
    margin-left: 0.5em;
  }
  .meta {
    font-size: 0.82rem;
  }
  .acts {
    display: flex;
    gap: 0.4rem;
    flex-wrap: wrap;
  }
  .editor {
    border: 1px solid var(--rule);
    border-radius: 8px;
    padding: 0.8rem 1.1rem 1.1rem;
  }
  h2,
  h3 {
    font-size: 1rem;
    margin: 0.6rem 0 0.4rem;
  }
  .row {
    display: flex;
    gap: 0.5rem;
    align-items: center;
    flex-wrap: wrap;
    margin-bottom: 0.45rem;
  }
  .grow {
    flex: 1;
    min-width: 8rem;
  }
  .keys {
    width: 3.5rem;
  }
  .keys.wide {
    width: 8rem;
    font-family: var(--mono);
  }
  .check {
    display: inline-flex;
    gap: 0.35rem;
    align-items: center;
  }
  textarea {
    width: 100%;
    font-family: var(--mono);
    font-size: 0.8rem;
  }
  .actions {
    display: flex;
    gap: 0.75rem;
    margin-top: 0.7rem;
  }
  .ok {
    color: var(--accent);
  }
</style>
