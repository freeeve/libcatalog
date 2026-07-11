<script lang="ts">
  // Command palette (Ctrl/Cmd+K): one fuzzy box over navigation
  // actions, "run macro" shortcuts, and jump-to-work (live search once the
  // query stops matching only actions). Modal owns the trap/Escape; the
  // input drives the RowList highlight.
  import { onMount } from "svelte";
  import { fetchMacros, fetchWorks } from "../lib/api";
  import { popScope, pushScope } from "../lib/keyboard";
  import { sequencer } from "../lib/sequence";
  import { navigate } from "../lib/router";
  import { paletteLabel, SCREENS } from "../lib/screens";
  import Modal from "./Modal.svelte";
  import RowList from "./RowList.svelte";
  import type { Macro, WorkSummary } from "../lib/types";

  let { onclose }: { onclose: () => void } = $props();

  interface Entry {
    id: string;
    label: string;
    hint?: string;
    run: () => void;
  }

  const SCOPE = "palette";
  const DEBOUNCE_MS = 200;
  const seq = sequencer();

  // Derived from the one screen table, so a new screen is reachable here the
  // day it routes -- the palette used to be a hand-maintained subset, and
  // answered "No matching commands." for three screens that existed
  //.
  const NAV: Entry[] = SCREENS.map((s) => ({
    id: `nav-${s.route}`,
    label: `Go to ${paletteLabel(s)}`,
    run: () => navigate(s.path),
  }));

  let q = $state("");
  let macros = $state<Macro[]>([]);
  let works = $state<WorkSummary[]>([]);
  let highlight = $state(0);
  let list = $state<{ move: (delta: number) => void } | null>(null);
  let timer: ReturnType<typeof setTimeout> | undefined;

  const entries = $derived.by(() => {
    const norm = q.trim().toLowerCase();
    const out: Entry[] = [];
    for (const e of NAV) {
      if (!norm || e.label.toLowerCase().includes(norm)) out.push(e);
    }
    for (const m of macros) {
      const label = `Run macro: ${m.label}`;
      if (!norm || label.toLowerCase().includes(norm)) {
        out.push({
          id: "macro-" + m.id,
          label,
          hint: m.shared ? "shared" : undefined,
          run: () => navigate(`/batch?macro=${encodeURIComponent(m.id)}`),
        });
      }
    }
    for (const w of works) {
      out.push({
        id: "work-" + w.WorkID,
        label: `Open: ${w.Title || w.WorkID}`,
        hint: w.WorkID,
        run: () => navigate(`/works/${encodeURIComponent(w.WorkID)}`),
      });
    }
    return out;
  });

  onMount(() => {
    pushScope(SCOPE);
    fetchMacros().then(
      (r) => (macros = r.macros ?? []),
      () => {},
    );
    return () => {
      popScope(SCOPE);
      clearTimeout(timer);
    };
  });

  function onInput(): void {
    highlight = 0;
    clearTimeout(timer);
    timer = setTimeout(() => void searchWorks(q), DEBOUNCE_MS);
  }

  async function searchWorks(query: string): Promise<void> {
    const t = seq.take();
    if (query.trim().length < 2) {
      works = [];
      return;
    }
    try {
      const page = await fetchWorks(query, 8);
      if (t.stale) return;
      works = (page.works ?? []).slice(0, 8);
    } catch {
      if (t.stale) return;
      works = [];
    }
  }

  function pick(e: Entry): void {
    onclose();
    e.run();
  }

  function onInputKeydown(ev: KeyboardEvent): void {
    if (ev.key === "ArrowDown") {
      ev.preventDefault();
      list?.move(1);
    } else if (ev.key === "ArrowUp") {
      ev.preventDefault();
      list?.move(-1);
    } else if (ev.key === "Enter") {
      ev.preventDefault();
      const e = entries[highlight];
      if (e) pick(e);
    }
  }
</script>

<Modal ariaLabel="Command palette" {onclose} width="38rem" placement="top">
  <input
    id="cp-q"
    type="search"
    data-autofocus
    bind:value={q}
    oninput={onInput}
    onkeydown={onInputKeydown}
    autocomplete="off"
    placeholder="Jump to a screen, run a macro, or find a work…"
    aria-label="Command"
  />
  <div class="options">
    <RowList
      bind:this={list}
      items={entries}
      bind:selected={highlight}
      getKey={(e) => e.id}
      ariaLabel="Commands"
      empty="No matching commands."
    >
      {#snippet row(e: Entry)}
        <button class="opt" onclick={() => pick(e)}>
          <span class="opt-label">{e.label}</span>
          {#if e.hint}<span class="opt-hint">{e.hint}</span>{/if}
        </button>
      {/snippet}
    </RowList>
  </div>
  <p class="muted foot">↑↓ to highlight · Enter to run · Esc to close</p>
</Modal>

<style>
  #cp-q {
    width: 100%;
    font-size: 1.05rem;
  }
  .options {
    margin-top: 0.5rem;
    max-height: 20rem;
    overflow-y: auto;
  }
  .opt {
    display: flex;
    width: 100%;
    gap: 0.8rem;
    align-items: baseline;
    text-align: left;
    background: none;
    border: 0;
    padding: 0.35rem 0.5rem;
    color: inherit;
  }
  .opt-label {
    flex: 1;
    font-weight: 600;
  }
  .opt-hint {
    font-family: var(--mono);
    font-size: 0.72rem;
    color: var(--ink-muted);
  }
  .foot {
    margin: 0.5rem 0 0;
    font-size: 0.78rem;
  }
</style>
