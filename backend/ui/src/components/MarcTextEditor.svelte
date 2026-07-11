<script lang="ts">
  // The text surface of the MARC editor: the whole record as an
  // editable mrk-style buffer -- plain monospace textarea, a line-number
  // gutter that flags parse errors, fixed-field lines openable in the
  // positional builder, and the field-clipboard chords working on the caret
  // line. Every clean parse flows into the same in-memory doc the grid
  // edits; a broken line keeps the buffer authoritative and reports
  // per-line, so nothing typed is ever discarded.
  import { onMount } from "svelte";
  import FixedFieldGrid from "./FixedFieldGrid.svelte";
  import { clipPeek, clipPush } from "../lib/fieldClipboard.svelte";
  import { bindKeys } from "../lib/keyboard";
  import { locFieldHelpUrl } from "../lib/lochelp";
  import { isFixedTag } from "../lib/marc";
  import { parseRecord, serializeField, serializeRecord, type MrkError } from "../lib/mrk";
  import type { MarcRecordDoc } from "../lib/types";

  let {
    record,
    knownLoss = {},
    scope,
    onchange,
    onvalid,
  }: {
    record: MarcRecordDoc;
    knownLoss?: Record<string, string>;
    /** Keyboard scope the caret-line field ops register in. */
    scope?: string;
    onchange: (record: MarcRecordDoc) => void;
    /** Reports whether the buffer currently parses (gates saving). */
    onvalid: (ok: boolean) => void;
  } = $props();

  let text = $state("");
  let errors = $state<MrkError[]>([]);
  let ta = $state<HTMLTextAreaElement | null>(null);
  let gutter = $state<HTMLElement | null>(null);
  /** 0-based buffer line open in the fixed-field builder, or null. */
  let building = $state<number | null>(null);

  /** The canonical text of the doc last agreed on with the parent; an
   *  outside change (a clipboard-pane paste, or the mount itself) serializes
   *  into the buffer, a change that round-trips to this stays the user's
   *  exact keystrokes. */
  let agreed = "";

  $effect(() => {
    const fresh = serializeRecord(record);
    if (fresh !== agreed) {
      agreed = fresh;
      text = fresh;
      errors = [];
      onvalid(true);
      building = null;
    }
  });

  const errorByLine = $derived(new Map(errors.map((e) => [e.line, e.message])));
  const lineCount = $derived(text.split("\n").length);

  /** Fixed-field lines (LDR/006/007/008) the builder can open. */
  const fixedLines = $derived(
    text.split("\n").flatMap((line, i) => {
      const tag = line.startsWith("LDR") ? "LDR" : line.slice(0, 3);
      return tag === "LDR" || isFixedTag(tag) ? [{ line: i, tag, value: line.slice(4) }] : [];
    }),
  );

  onMount(() => {
    if (!scope) return;
    return bindKeys(scope, {
      "alt+c": {
        description: "copy the caret line's field to the field clipboard",
        legend: "copy field",
        keyLabel: "alt+c/x/v",
        allowInInputs: true,
        handler: copyLine,
      },
      "alt+x": {
        description: "cut the caret line's field to the field clipboard",
        legendHidden: true,
        allowInInputs: true,
        handler: cutLine,
      },
      "alt+v": {
        description: "paste the last clipboard field below the caret line",
        legendHidden: true,
        allowInInputs: true,
        handler: pasteLine,
      },
      "alt+h": {
        description: "open the LOC MARC 21 page for the caret line's tag",
        legend: "field help",
        allowInInputs: true,
        handler: helpLine,
      },
    });
  });

  function handleInput(): void {
    const res = parseRecord(text, record, knownLoss);
    errors = res.errors;
    if (res.record) {
      agreed = serializeRecord(res.record);
      onchange(res.record);
    }
    onvalid(errors.length === 0);
  }

  function syncScroll(): void {
    if (gutter && ta) gutter.scrollTop = ta.scrollTop;
  }

  function caretLine(): number {
    return text.slice(0, ta?.selectionStart ?? 0).split("\n").length - 1;
  }

  /** The caret line parsed as a lone field; null for LDR/blank/broken. */
  function caretField() {
    const line = text.split("\n")[caretLine()] ?? "";
    if (line.trim() === "" || line.startsWith("LDR")) return null;
    return parseRecord(line, record, knownLoss).record?.fields[0] ?? null;
  }

  function copyLine(): void {
    const f = caretField();
    if (f) clipPush(f);
  }

  function cutLine(): void {
    const f = caretField();
    if (!f) return;
    clipPush(f);
    const lines = text.split("\n");
    lines.splice(caretLine(), 1);
    text = lines.join("\n");
    handleInput();
  }

  function pasteLine(): void {
    const f = clipPeek();
    if (!f) return;
    const lines = text.split("\n");
    lines.splice(caretLine() + 1, 0, serializeField(f));
    text = lines.join("\n");
    handleInput();
  }

  function helpLine(): void {
    const line = text.split("\n")[caretLine()] ?? "";
    const tag = line.startsWith("LDR") ? "LDR" : line.slice(0, 3);
    const url = locFieldHelpUrl(tag);
    if (url) window.open(url, "_blank", "noreferrer");
  }

  /** The builder's edit lands back in its buffer line. */
  function applyFixed(lineIdx: number, tag: string, value: string): void {
    const lines = text.split("\n");
    lines[lineIdx] = tag === "LDR" ? `LDR ${value}` : `${tag} ${value}`;
    text = lines.join("\n");
    handleInput();
  }
</script>

<div class="editor">
  <div class="buffer">
    <div class="gutter" bind:this={gutter} aria-hidden="true">
      {#each { length: lineCount } as _, i (i)}
        <div class="ln" class:err={errorByLine.has(i + 1)} title={errorByLine.get(i + 1) ?? ""}>{i + 1}</div>
      {/each}
    </div>
    <textarea
      bind:this={ta}
      bind:value={text}
      oninput={handleInput}
      onscroll={syncScroll}
      rows={Math.min(Math.max(lineCount + 1, 8), 30)}
      spellcheck="false"
      autocapitalize="off"
      autocomplete="off"
      aria-label="MARC record as text"
      aria-invalid={errors.length > 0}
    ></textarea>
  </div>

  {#if errors.length > 0}
    <ul class="errors" role="alert">
      {#each errors as e (e.line + e.message)}
        <li><span class="mono">line {e.line}</span> -- {e.message}</li>
      {/each}
    </ul>
  {/if}

  <p class="fixedrow">
    {#each fixedLines as fl (fl.line)}
      <button class="button button--quiet mini" aria-expanded={building === fl.line} onclick={() => (building = building === fl.line ? null : fl.line)}>
        {fl.tag} positions <span class="muted">(line {fl.line + 1})</span>
      </button>
    {/each}
    <span class="muted hint">Alt+C/X/V field clipboard on the caret line · Alt+H field help · "?" lists every chord</span>
  </p>
  {#if building !== null}
    {@const fl = fixedLines.find((x) => x.line === building)}
    {#if fl}
      <FixedFieldGrid tag={fl.tag} value={fl.value} onchange={(v) => applyFixed(fl.line, fl.tag, v)} />
    {/if}
  {/if}
</div>

<style>
  .buffer {
    display: flex;
    border: 1px solid var(--rule);
    border-radius: 6px;
    overflow: hidden;
    background: var(--surface);
  }
  .gutter {
    flex: none;
    width: 2.6rem;
    max-height: 60vh;
    overflow: hidden;
    text-align: right;
    padding: 0.45rem 0.4rem 0.45rem 0;
    border-right: 1px solid var(--rule);
    color: var(--ink-muted);
    font-family: var(--mono);
    font-size: 0.78rem;
    line-height: 1.5;
    user-select: none;
  }
  .ln.err {
    color: var(--danger);
    font-weight: 700;
    background: var(--tint-danger);
  }
  textarea {
    flex: 1;
    border: 0;
    border-radius: 0;
    resize: vertical;
    padding: 0.45rem 0.6rem;
    max-height: 60vh;
    font-family: var(--mono);
    font-size: 0.78rem;
    line-height: 1.5;
    white-space: pre;
    overflow-wrap: normal;
    overflow-x: auto;
  }
  .errors {
    margin: 0.4rem 0 0;
    padding-left: 1.1rem;
    font-size: 0.8rem;
    color: var(--danger);
  }
  .mono {
    font-family: var(--mono);
  }
  .fixedrow {
    display: flex;
    gap: 0.4rem;
    align-items: center;
    flex-wrap: wrap;
    margin: 0.5rem 0 0.2rem;
  }
  .mini {
    font-size: 0.72rem;
    padding: 0.05em 0.6em;
  }
  .hint {
    font-size: 0.75rem;
  }
</style>
