<script lang="ts">
  // The MARC editing grid: one row per field -- tag, indicators,
  // and the "$a … $b …" subfield line (control fields edit their raw value)
  // -- keyboard-first: Enter in a row's line inserts a fresh row below, tag
  // order is the cataloger's own. Fixed fields (leader, 006/007/008) expand
  // into positional builders. Lossy tags carry a non-blocking warning: their
  // edits persist verbatim, not modeled. The field-op family --
  // copy/cut/paste through the app clipboard, duplicate, © ℗ $ inserts, LOC
  // help -- registers in the host screen's keyboard scope with
  // allowInInputs, so the chords fire from the grid's inputs and stay
  // remappable, legible in the legend, and listed in the "?" overlay.
  import { onMount } from "svelte";
  import FixedFieldGrid from "./FixedFieldGrid.svelte";
  import { clipPeek, clipPush } from "../lib/fieldClipboard.svelte";
  import { bindKeys } from "../lib/keyboard";
  import { locFieldHelpUrl } from "../lib/lochelp";
  import { blankField, isControlTag, isFixedTag, lineToSubfields, subfieldsToLine } from "../lib/marc";
  import type { MarcRecordDoc, MarcField } from "../lib/types";

  let {
    record,
    knownLoss = {},
    scope,
    onchange,
  }: {
    record: MarcRecordDoc;
    knownLoss?: Record<string, string>;
    /** Keyboard scope the field ops register in (the host screen's). */
    scope?: string;
    onchange: (record: MarcRecordDoc) => void;
  } = $props();

  const FIDELITY_URL = "https://github.com/freeeve/libcat/blob/main/docs/marc-fidelity.md";

  let expanded = $state<Record<number, boolean>>({});
  /** The row the field ops act on: the last one focus visited. */
  let active = $state(0);

  onMount(() => {
    if (!scope) return;
    return bindKeys(scope, {
      "alt+c": {
        description: "copy the current field to the field clipboard",
        legend: "copy field",
        keyLabel: "alt+c/x/v",
        allowInInputs: true,
        handler: copyField,
      },
      "alt+x": {
        description: "cut the current field to the field clipboard",
        legendHidden: true,
        allowInInputs: true,
        handler: cutField,
      },
      "alt+v": {
        description: "paste the last clipboard field below the current row",
        legendHidden: true,
        allowInInputs: true,
        handler: pasteField,
      },
      "alt+d": {
        description: "duplicate the current field on the next line",
        legendHidden: true,
        allowInInputs: true,
        handler: () => duplicateField(active),
      },
      "alt+g": {
        description: "insert the copyright symbol ©",
        legendHidden: true,
        allowInInputs: true,
        handler: () => insertAtCursor("©"),
      },
      "alt+r": {
        description: "insert the phonogram symbol ℗",
        legendHidden: true,
        allowInInputs: true,
        handler: () => insertAtCursor("℗"),
      },
      "alt+k": {
        description: "insert a subfield delimiter ($)",
        legendHidden: true,
        allowInInputs: true,
        handler: () => insertAtCursor("$"),
      },
      "alt+h": {
        description: "open the LOC MARC 21 page for the current field",
        legend: "field help",
        allowInInputs: true,
        handler: helpField,
      },
    });
  });

  function copyField(): void {
    const f = record.fields[active];
    if (f) clipPush($state.snapshot(f));
  }

  function cutField(): void {
    const f = record.fields[active];
    if (!f) return;
    clipPush($state.snapshot(f));
    removeRow(active);
  }

  function pasteField(): void {
    const f = clipPeek();
    if (f) insertBelow(Math.min(active, record.fields.length - 1), f);
  }

  function duplicateField(i: number): void {
    const f = record.fields[i];
    if (f) insertBelow(i, structuredClone($state.snapshot(f)));
  }

  function helpField(): void {
    const f = record.fields[active];
    const url = f && locFieldHelpUrl(f.tag);
    if (url) window.open(url, "_blank", "noreferrer");
  }

  /** Types text at the caret of the grid input holding focus, firing the
   *  input's change handler so the edit lands in the record. */
  function insertAtCursor(text: string): void {
    const el = document.activeElement;
    if (!(el instanceof HTMLInputElement) || !el.closest(".grid")) return;
    const start = el.selectionStart ?? el.value.length;
    el.setRangeText(text, start, el.selectionEnd ?? start, "end");
    el.dispatchEvent(new Event("change", { bubbles: true }));
  }

  function update(fields: MarcField[]): void {
    onchange({ ...record, fields });
  }

  function setField(i: number, patch: Partial<MarcField>): void {
    update(record.fields.map((f, j) => (j === i ? { ...f, ...patch } : f)));
  }

  function setTag(i: number, tag: string): void {
    const f = record.fields[i];
    const next: MarcField = { ...f, tag, lossy: knownLoss[tag] ?? "" };
    if (isControlTag(tag) && !isControlTag(f.tag)) {
      next.value = subfieldsToLine(f.subfields);
      next.subfields = undefined;
      next.ind1 = next.ind2 = undefined;
    } else if (!isControlTag(tag) && isControlTag(f.tag)) {
      next.subfields = lineToSubfields(f.value ?? "");
      next.value = undefined;
      next.ind1 = next.ind2 = " ";
    }
    update(record.fields.map((cur, j) => (j === i ? next : cur)));
  }

  function insertBelow(i: number, field?: MarcField): void {
    const next = [...record.fields];
    next.splice(i + 1, 0, field ?? blankField());
    update(next);
    queueMicrotask(() => focusRow(i + 1));
  }

  function removeRow(i: number): void {
    update(record.fields.filter((_, j) => j !== i));
  }

  function onLineKeydown(ev: KeyboardEvent, i: number): void {
    if (ev.key === "Enter") {
      ev.preventDefault();
      insertBelow(i);
    }
  }

  function focusRow(i: number): void {
    document.getElementById(`marc-tag-${record.node}-${i}`)?.focus();
  }
</script>

<div class="grid" role="group" aria-label="MARC fields">
  <div class="row head">
    <span class="tag">Tag</span><span class="ind">I1</span><span class="ind">I2</span><span>Value</span>
  </div>

  <div class="row">
    <span class="tag mono">LDR</span>
    <span class="ind"></span><span class="ind"></span>
    <div class="val">
      <input
        class="line mono"
        aria-label="Leader"
        value={record.leader}
        onchange={(ev) => onchange({ ...record, leader: (ev.currentTarget as HTMLInputElement).value })}
      />
      <button class="button button--quiet mini" onclick={() => (expanded = { ...expanded, [-1]: !expanded[-1] })}>
        {expanded[-1] ? "Hide" : "Positions"}
      </button>
      {#if expanded[-1]}
        <FixedFieldGrid tag="LDR" value={record.leader} onchange={(v) => onchange({ ...record, leader: v })} />
      {/if}
    </div>
    <span class="acts">
      <a class="help" href={locFieldHelpUrl("LDR")} target="_blank" rel="noreferrer" title="MARC 21 leader documentation" aria-label="MARC 21 leader documentation">?</a>
    </span>
  </div>

  {#each record.fields as f, i (i)}
    <div class="row" class:lossy={!!(f.lossy || knownLoss[f.tag])} onfocusin={() => (active = i)}>
      <input
        id={`marc-tag-${record.node}-${i}`}
        class="tag mono"
        aria-label="Tag"
        maxlength="3"
        value={f.tag}
        onchange={(ev) => setTag(i, (ev.currentTarget as HTMLInputElement).value)}
      />
      {#if isControlTag(f.tag)}
        <span class="ind"></span><span class="ind"></span>
      {:else}
        <input class="ind mono" aria-label="Indicator 1" maxlength="1" value={f.ind1 ?? " "}
          onchange={(ev) => setField(i, { ind1: (ev.currentTarget as HTMLInputElement).value || " " })} />
        <input class="ind mono" aria-label="Indicator 2" maxlength="1" value={f.ind2 ?? " "}
          onchange={(ev) => setField(i, { ind2: (ev.currentTarget as HTMLInputElement).value || " " })} />
      {/if}
      <div class="val">
        {#if isControlTag(f.tag)}
          <input class="line mono" aria-label={"Field " + f.tag + " value"} value={f.value ?? ""}
            onkeydown={(ev) => onLineKeydown(ev, i)}
            onchange={(ev) => setField(i, { value: (ev.currentTarget as HTMLInputElement).value })} />
          {#if isFixedTag(f.tag)}
            <button class="button button--quiet mini" onclick={() => (expanded = { ...expanded, [i]: !expanded[i] })}>
              {expanded[i] ? "Hide" : "Positions"}
            </button>
            {#if expanded[i]}
              <FixedFieldGrid tag={f.tag} value={f.value ?? ""} onchange={(v) => setField(i, { value: v })} />
            {/if}
          {/if}
        {:else}
          <input class="line mono" aria-label={"Field " + f.tag + " subfields"} value={subfieldsToLine(f.subfields)}
            onkeydown={(ev) => onLineKeydown(ev, i)}
            onchange={(ev) => setField(i, { subfields: lineToSubfields((ev.currentTarget as HTMLInputElement).value) })} />
        {/if}
        {#if f.lossy || knownLoss[f.tag]}
          <p class="warn">
            Crosswalk-lossy tag: {f.lossy || knownLoss[f.tag]} -- edits persist verbatim
            (<a href={FIDELITY_URL} target="_blank" rel="noreferrer">fidelity table</a>).
          </p>
        {/if}
      </div>
      <span class="acts">
        {#if locFieldHelpUrl(f.tag)}
          <a class="help" href={locFieldHelpUrl(f.tag)} target="_blank" rel="noreferrer"
            title={"MARC 21 documentation for " + f.tag} aria-label={"MARC 21 documentation for " + f.tag}>?</a>
        {/if}
        <button class="button button--quiet mini" title="Duplicate field (Alt+D)"
          onclick={() => duplicateField(i)}>Dup</button>
        <button class="button button--quiet mini" onclick={() => removeRow(i)}>Del</button>
      </span>
    </div>
  {/each}

  <p>
    <button class="button button--quiet" onclick={() => insertBelow(record.fields.length - 1)}>Add field</button>
    <span class="muted hint">Enter in a value inserts a row below · Alt+D duplicates · Alt+C/X/V field clipboard · "?" lists every chord</span>
  </p>
</div>

<style>
  .grid {
    margin: 0.5rem 0;
  }
  .row {
    display: grid;
    grid-template-columns: 3.6rem 1.6rem 1.6rem 1fr auto;
    gap: 0.4rem;
    align-items: start;
    padding: 0.15rem 0;
    border-bottom: 1px solid var(--rule);
  }
  .row.head {
    font-size: 0.72rem;
    color: var(--ink-muted);
    border-bottom-color: var(--ink-muted);
  }
  .row.lossy {
    background: color-mix(in srgb, var(--surface) 80%, #c77d0a 8%);
  }
  .mono {
    font-family: var(--mono);
  }
  .tag {
    width: 3.4rem;
  }
  .ind {
    width: 1.5rem;
    text-align: center;
  }
  .val {
    display: block;
  }
  .line {
    width: 100%;
    font-size: 0.85rem;
  }
  .mini {
    font-size: 0.72rem;
    padding: 0.05em 0.6em;
  }
  .warn {
    font-size: 0.78rem;
    margin: 0.15rem 0 0.1rem;
    color: inherit;
  }
  .acts {
    display: inline-flex;
    gap: 0.25rem;
    align-items: center;
  }
  .help {
    font-family: var(--mono);
    font-size: 0.72rem;
    font-weight: 700;
    color: var(--ink-muted);
    border: 1px solid var(--rule);
    border-radius: 999px;
    width: 1.15rem;
    height: 1.15rem;
    display: inline-flex;
    align-items: center;
    justify-content: center;
    text-decoration: none;
  }
  .help:hover {
    color: var(--accent);
    border-color: var(--accent);
  }
  .hint {
    font-size: 0.78rem;
  }
</style>
