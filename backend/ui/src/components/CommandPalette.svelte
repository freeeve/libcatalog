<script lang="ts">
  // Command palette (Ctrl/Cmd+K, tasks/047): one fuzzy box over navigation
  // actions, "run macro" shortcuts, and jump-to-work (live search once the
  // query stops matching only actions). Focus is trapped while open; Escape
  // closes; Enter runs the highlighted entry.
  import { onMount } from "svelte";
  import { fetchMacros, fetchWorks } from "../lib/api";
  import { popScope, pushScope } from "../lib/keyboard";
  import { navigate } from "../lib/router";
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

  const NAV: Entry[] = [
    { id: "nav-works", label: "Go to Works", run: () => navigate("/works") },
    { id: "nav-authorities", label: "Go to Authorities", run: () => navigate("/authorities") },
    { id: "nav-queue", label: "Go to Queue", run: () => navigate("/queue") },
    { id: "nav-promotions", label: "Go to Promotions", run: () => navigate("/promotions") },
    { id: "nav-batch", label: "Go to Batch operations", run: () => navigate("/batch") },
    { id: "nav-macros", label: "Go to Macros", run: () => navigate("/macros") },
    { id: "nav-exports", label: "Go to Exports", run: () => navigate("/exports") },
    { id: "nav-copycat", label: "Go to Copy cataloging (import)", run: () => navigate("/copycat") },
    { id: "nav-duplicates", label: "Go to Duplicates", run: () => navigate("/duplicates") },
    { id: "nav-dashboard", label: "Go to Dashboard", run: () => navigate("/") },
  ];

  let q = $state("");
  let macros = $state<Macro[]>([]);
  let works = $state<WorkSummary[]>([]);
  let highlight = $state(0);
  let panel = $state<HTMLElement | null>(null);
  let inputEl = $state<HTMLInputElement | null>(null);
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
    const opener = document.activeElement as HTMLElement | null;
    pushScope(SCOPE);
    inputEl?.focus();
    fetchMacros().then(
      (r) => (macros = r.macros ?? []),
      () => {},
    );
    return () => {
      popScope(SCOPE);
      clearTimeout(timer);
      opener?.focus?.();
    };
  });

  function onInput(): void {
    highlight = 0;
    clearTimeout(timer);
    timer = setTimeout(() => void searchWorks(q), DEBOUNCE_MS);
  }

  async function searchWorks(query: string): Promise<void> {
    if (query.trim().length < 2) {
      works = [];
      return;
    }
    try {
      works = ((await fetchWorks(query, 8)).works ?? []).slice(0, 8);
    } catch {
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
      move(1);
    } else if (ev.key === "ArrowUp") {
      ev.preventDefault();
      move(-1);
    } else if (ev.key === "Enter") {
      ev.preventDefault();
      const e = entries[highlight];
      if (e) pick(e);
    }
  }

  function move(delta: number): void {
    if (entries.length === 0) return;
    highlight = Math.min(entries.length - 1, Math.max(0, highlight + delta));
    document.getElementById(`cp-opt-${highlight}`)?.scrollIntoView({ block: "nearest" });
  }

  function onPanelKeydown(ev: KeyboardEvent): void {
    if (ev.key === "Escape") {
      ev.stopPropagation();
      onclose();
      return;
    }
    if (ev.key !== "Tab" || !panel) return;
    const focusables = panel.querySelectorAll<HTMLElement>('button, input, [tabindex]:not([tabindex="-1"])');
    if (focusables.length === 0) return;
    const first = focusables[0];
    const last = focusables[focusables.length - 1];
    if (ev.shiftKey && document.activeElement === first) {
      ev.preventDefault();
      last.focus();
    } else if (!ev.shiftKey && document.activeElement === last) {
      ev.preventDefault();
      first.focus();
    }
  }
</script>

<div class="scrim">
  <div class="panel" role="dialog" aria-modal="true" aria-label="Command palette" tabindex="-1" bind:this={panel} onkeydown={onPanelKeydown}>
    <input
      id="cp-q"
      type="search"
      bind:this={inputEl}
      bind:value={q}
      oninput={onInput}
      onkeydown={onInputKeydown}
      autocomplete="off"
      placeholder="Jump to a screen, run a macro, or find a work…"
      aria-label="Command"
    />
    <ul class="options" aria-label="Commands">
      {#each entries as e, i (e.id)}
        <li id={"cp-opt-" + i} class:highlight={i === highlight}>
          <button class="opt" onclick={() => pick(e)} onfocus={() => (highlight = i)}>
            <span class="opt-label">{e.label}</span>
            {#if e.hint}<span class="opt-hint">{e.hint}</span>{/if}
          </button>
        </li>
      {:else}
        <li class="muted empty">No matching commands.</li>
      {/each}
    </ul>
    <p class="muted foot">↑↓ to highlight · Enter to run · Esc to close</p>
  </div>
</div>

<style>
  .scrim {
    position: fixed;
    inset: 0;
    background: rgba(20, 22, 25, 0.55);
    display: grid;
    place-items: start center;
    padding-top: 12vh;
    z-index: 50;
  }
  .panel {
    background: var(--bg);
    border: 1px solid var(--rule);
    border-radius: 8px;
    padding: 0.8rem 1rem;
    width: min(38rem, 94vw);
  }
  #cp-q {
    width: 100%;
    font-size: 1.05rem;
  }
  .options {
    list-style: none;
    margin: 0.5rem 0 0;
    padding: 0;
    max-height: 20rem;
    overflow-y: auto;
  }
  .options li {
    border: 1px solid transparent;
  }
  .options li.highlight {
    border-color: var(--accent);
    border-radius: 4px;
    background: var(--surface);
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
  .empty {
    padding: 0.5rem;
  }
  .foot {
    margin: 0.5rem 0 0;
    font-size: 0.78rem;
  }
</style>
