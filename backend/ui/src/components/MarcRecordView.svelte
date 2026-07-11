<script lang="ts">
  // Read-only rendering of one MARC record: the line-per-field
  // view shared by the live preview pane, the copycat result preview, and
  // the staged-batch review. Tags link to their LOC MARC 21 documentation
  // page when one exists; lossy tags render muted with the fidelity reason.
  import { locFieldHelpUrl, type MarcDocKind } from "../lib/lochelp";
  import type { MarcField, MarcRecordDoc } from "../lib/types";

  let {
    record,
    knownLoss = {},
    kind = "bibliographic",
    changed,
  }: {
    record: MarcRecordDoc;
    knownLoss?: Record<string, string>;
    kind?: MarcDocKind;
    changed?: (f: MarcField) => boolean;
  } = $props();

  const leaderHelp = $derived(locFieldHelpUrl("LDR", kind));
</script>

<p class="fl mono ldr">
  {#if leaderHelp}<a class="tag" href={leaderHelp} target="_blank" rel="noreferrer" title="MARC 21 leader documentation">LDR</a>{:else}<span class="tag">LDR</span>{/if}<span class="ind">&nbsp;&nbsp;</span>{record.leader}
</p>
{#each record.fields as f, fi (fi)}
  {@const help = locFieldHelpUrl(f.tag, kind)}
  <p
    class="fl mono"
    class:changed={changed?.(f) ?? false}
    class:lossy={!!(f.lossy || knownLoss[f.tag])}
    title={f.lossy || knownLoss[f.tag] || ""}
  >
    {#if help}<a class="tag" href={help} target="_blank" rel="noreferrer" title={"MARC 21 documentation for " + f.tag}>{f.tag}</a>{:else}<span class="tag">{f.tag}</span>{/if}<span class="ind">{f.ind1 || " "}{f.ind2 || " "}</span>{#if f.value}{f.value}{:else}{#each f.subfields ?? [] as sf, si (si)}<span class="sf">${sf.code}</span>{" " + sf.value + " "}{/each}{/if}
  </p>
{/each}

<style>
  .fl {
    margin: 0;
    padding: 0.08rem 0.3rem;
    font-size: 0.78rem;
    line-height: 1.45;
    word-break: break-word;
  }
  .mono {
    font-family: var(--mono);
  }
  .ldr {
    color: var(--ink-muted);
  }
  .tag {
    font-weight: 700;
    margin-right: 0.5em;
  }
  a.tag {
    color: inherit;
    text-decoration-color: var(--rule);
    text-underline-offset: 0.2em;
  }
  a.tag:hover {
    color: var(--accent);
    text-decoration-color: var(--accent);
  }
  .ind {
    color: var(--ink-muted);
    margin-right: 0.5em;
    white-space: pre;
  }
  .sf {
    color: var(--accent);
    font-weight: 600;
  }
  .fl.changed {
    background: var(--tint-ok, rgba(46, 160, 67, 0.12));
    box-shadow: inset 2px 0 0 var(--accent);
  }
  .fl.lossy {
    opacity: 0.65;
  }
</style>
