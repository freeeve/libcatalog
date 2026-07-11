<script lang="ts">
  // Tag typeahead: merges distinct grain-tree tags (with carry counts -- the
  // convergence nudge) and accepted folk vocabulary terms. Selection emits
  // the plain tag string through onselect; oninput tracks free typing so a
  // form can also take unlisted tags.
  import { fetchTags, searchFolkTerms } from "../lib/api";
  import { sequencer } from "../lib/sequence";

  let {
    id = "tag-input",
    label = "Tag",
    placeholder = "Type a tag…",
    hideLabel = false,
    onselect,
    oninput,
  }: {
    id?: string;
    label?: string;
    placeholder?: string;
    hideLabel?: boolean;
    onselect: (tag: string) => void;
    oninput?: (q: string) => void;
  } = $props();

  const DEBOUNCE_MS = 200;
  const seq = sequencer();

  interface Option {
    tag: string;
    count?: number;
    folk: boolean;
  }

  let q = $state("");
  let options = $state<Option[]>([]);
  let highlight = $state(0);
  let open = $state(false);
  let timer: ReturnType<typeof setTimeout> | undefined;

  function onType(): void {
    oninput?.(q);
    clearTimeout(timer);
    timer = setTimeout(() => void search(q), DEBOUNCE_MS);
  }

  async function search(query: string): Promise<void> {
    const t = seq.take();
    const trimmed = query.trim();
    if (!trimmed) {
      options = [];
      open = false;
      return;
    }
    const [tags, folk] = await Promise.all([
      fetchTags(trimmed).catch(() => ({ tags: [] })),
      searchFolkTerms(trimmed).catch(() => ({ terms: [] })),
    ]);
    if (t.stale) return;
    const merged = new Map<string, Option>();
    for (const t of tags.tags ?? []) merged.set(t.tag, { tag: t.tag, count: t.count, folk: false });
    for (const f of folk.terms ?? []) {
      const existing = merged.get(f.id);
      if (existing) existing.folk = true;
      else merged.set(f.id, { tag: f.id, folk: true });
    }
    options = [...merged.values()];
    highlight = 0;
    // Open even with zero matches: the "Create tag" row makes minting a
    // new tag a visible affordance, not an implicit keystroke.
    open = options.length > 0 || trimmed !== "";
  }

  // A typed value matching no suggestion is creatable -- free tagging is
  // the point of the field.
  const creatable = $derived(q.trim() !== "" && !options.some((o) => o.tag.toLowerCase() === q.trim().toLowerCase()));
  const rowCount = $derived(options.length + (creatable ? 1 : 0));

  function choose(tag: string): void {
    q = tag;
    open = false;
    oninput?.(tag);
    onselect(tag);
  }

  function onKeydown(ev: KeyboardEvent): void {
    if (ev.key === "Enter") {
      // Enter always commits: the highlighted suggestion when
      // one is picked, else the raw trimmed value as a new tag.
      ev.preventDefault();
      if (open && highlight < options.length && options[highlight]) {
        choose(options[highlight].tag);
      } else if (q.trim() !== "") {
        choose(q.trim());
      }
      return;
    }
    if (!open) return;
    if (ev.key === "ArrowDown") {
      ev.preventDefault();
      highlight = Math.min(rowCount - 1, highlight + 1);
    } else if (ev.key === "ArrowUp") {
      ev.preventDefault();
      highlight = Math.max(0, highlight - 1);
    } else if (ev.key === "Escape") {
      open = false;
    }
  }
</script>

<div class="taginput">
  <label for={id} class:sr-only={hideLabel}>{label}</label>
  <input {id} type="text" bind:value={q} oninput={onType} onkeydown={onKeydown} {placeholder} autocomplete="off" />
  {#if open}
    <ul class="menu" aria-label="Tag suggestions">
      {#each options as o, i (o.tag)}
        <li class:highlight={i === highlight}>
          <button type="button" class="opt" onclick={() => choose(o.tag)} onfocus={() => (highlight = i)}>
            <span class="name">{o.tag}{#if o.count} ({o.count}){/if}</span>
            {#if o.folk}<span class="folkmark">folk</span>{/if}
          </button>
        </li>
      {/each}
      {#if creatable}
        <li class:highlight={highlight === options.length}>
          <button type="button" class="opt" onclick={() => choose(q.trim())} onfocus={() => (highlight = options.length)}>
            <span class="name">Create tag “{q.trim()}”</span>
          </button>
        </li>
      {/if}
    </ul>
  {/if}
</div>

<style>
  .taginput {
    position: relative;
    display: inline-block;
    min-width: 18rem;
  }
  label {
    display: block;
    font-size: 0.85rem;
    font-weight: 600;
    margin-bottom: 0.2rem;
  }
  label.sr-only {
    position: absolute;
    width: 1px;
    height: 1px;
    overflow: hidden;
    clip-path: inset(50%);
    white-space: nowrap;
  }
  input {
    width: 100%;
  }
  .menu {
    position: absolute;
    left: 0;
    right: 0;
    top: 100%;
    z-index: 10;
    list-style: none;
    margin: 0.15rem 0 0;
    padding: 0.15rem;
    background: var(--bg);
    border: 1px solid var(--rule);
    border-radius: 6px;
    box-shadow: 0 4px 14px rgba(20, 22, 25, 0.15);
    max-height: 16rem;
    overflow-y: auto;
  }
  .menu li.highlight {
    background: var(--surface);
    box-shadow: inset 3px 0 0 var(--accent);
  }
  .opt {
    display: flex;
    align-items: baseline;
    justify-content: space-between;
    gap: 0.6rem;
    width: 100%;
    text-align: left;
    background: none;
    border: 0;
    padding: 0.3rem 0.5rem;
    color: inherit;
  }
  .name {
    font-weight: 600;
  }
  .folkmark {
    font-size: 0.7rem;
    font-weight: 600;
    letter-spacing: 0.03em;
    padding: 0.05em 0.5em;
    border-radius: 999px;
    background: #e3edf9;
    color: #1c4f8a;
    border: 1px solid #bcd3ef;
  }
</style>
