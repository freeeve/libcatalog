<script lang="ts">
  // Editable rendering of one resource's profile fields. The SPA knows the
  // shipped profile shapes, so each field maps to an add affordance and
  // per-value removal; the server owns the actual override semantics
  // (removing a feed value becomes an override there). Staged edits render
  // optimistically: pending values carry an undo, suppressed values strike
  // through before the save even lands.
  import ProvenanceBadge from "./ProvenanceBadge.svelte";
  import TagInput from "./TagInput.svelte";
  import VocabPicker from "./VocabPicker.svelte";
  import { valueKey } from "../lib/ops";
  import { bestLabel } from "../lib/vocab";
  import type { FieldValue, Op, OpValue, ResourceDoc, Term } from "../lib/types";

  type FieldKind = "single" | "langLiteral" | "iri" | "vocab" | "tag" | "literal";

  interface FieldSpec {
    path: string;
    label: string;
    kind: FieldKind;
    hint?: string;
  }

  // The shipped work-monograph and instance-ebook shapes (profiles/defaults).
  const WORK_FIELDS: FieldSpec[] = [
    { path: "title", label: "Title", kind: "single" },
    { path: "subtitle", label: "Subtitle", kind: "single" },
    { path: "summary", label: "Summary", kind: "langLiteral" },
    { path: "language", label: "Language", kind: "iri", hint: "http://id.loc.gov/vocabulary/languages/eng" },
    { path: "subjects", label: "Subjects", kind: "vocab" },
    { path: "tags", label: "Tags", kind: "tag" },
  ];
  const INSTANCE_FIELDS: FieldSpec[] = [
    { path: "isbn", label: "Identifiers", kind: "literal", hint: "9780000000000" },
    { path: "media", label: "Media type", kind: "iri", hint: "http://rdaregistry.info/termList/RDAMediaType/1003" },
    { path: "carrier", label: "Carrier type", kind: "iri", hint: "http://rdaregistry.info/termList/RDACarrierType/1018" },
  ];

  let {
    res,
    resource,
    kind,
    ops,
    onstage,
    onunstage,
  }: {
    res: ResourceDoc;
    resource: string; // op resource: "work" or the instance id
    kind: "work" | "instance";
    ops: Op[]; // every staged op; filtered to this resource here
    onstage: (op: Op) => void;
    onunstage: (op: Op) => void;
  } = $props();

  // kind is fixed for a mounted form (work forms never become instances).
  // svelte-ignore state_referenced_locally
  const specs = kind === "work" ? WORK_FIELDS : INSTANCE_FIELDS;
  // svelte-ignore state_referenced_locally
  const heading = kind === "work" ? "h2" : "h4";

  let entry = $state(Object.fromEntries(specs.map((s) => [s.path, { v: "", lang: "" }])));
  let pickerFor = $state<string | null>(null);
  let pickedLabels = $state<Record<string, string>>({});

  const mine = $derived(ops.filter((o) => o.resource === resource));
  const extraFields = $derived(
    Object.entries(res.fields)
      .filter(([path]) => !specs.some((s) => s.path === path))
      .sort(([a], [b]) => a.localeCompare(b)),
  );

  /** "subjectLabels" -> "Subject labels" for fields outside the shipped shape. */
  function prettify(path: string): string {
    const words = path.replace(/([a-z0-9])([A-Z])/g, "$1 $2").toLowerCase();
    return words.charAt(0).toUpperCase() + words.slice(1);
  }

  function fieldOps(path: string): Op[] {
    return mine.filter((o) => o.path === path);
  }

  interface PendingValue {
    value: OpValue;
    op: Op;
  }

  /** Values a staged add/set would create, paired with the op for undo. */
  function pendingAdds(path: string): PendingValue[] {
    const out: PendingValue[] = [];
    for (const op of fieldOps(path)) {
      if (op.action === "add" && op.value) out.push({ value: op.value, op });
      if (op.action === "set") for (const v of op.values ?? []) out.push({ value: v, op });
    }
    return out;
  }

  /** The staged op that suppresses fv, if any: a matching remove, or a
   *  set/clear replacing the whole field. */
  function removalOf(path: string, fv: FieldValue): Op | undefined {
    const key = valueKey({ v: fv.v, lang: fv.lang, iri: fv.iri });
    return fieldOps(path).find(
      (o) => o.action === "set" || o.action === "clear" || (o.action === "remove" && o.value && valueKey(o.value) === key),
    );
  }

  function stageRemove(path: string, fv: FieldValue): void {
    const value: OpValue = { v: fv.v };
    if (fv.lang) value.lang = fv.lang;
    if (fv.iri) value.iri = true;
    onstage({ resource, path, action: "remove", value });
  }

  function submitEntry(spec: FieldSpec): void {
    const box = entry[spec.path];
    const v = box.v.trim();
    if (!v) return;
    const value: OpValue = { v };
    if (spec.kind === "langLiteral" && box.lang.trim()) value.lang = box.lang.trim();
    if (spec.kind === "iri") value.iri = true;
    if (spec.kind === "single") onstage({ resource, path: spec.path, action: "set", values: [value] });
    else onstage({ resource, path: spec.path, action: "add", value });
    box.v = "";
  }

  function subjectPicked(term: Term): void {
    const path = pickerFor;
    pickerFor = null;
    if (!path) return;
    pickedLabels[term.id] = bestLabel(term);
    onstage({ resource, path, action: "add", value: { v: term.id, iri: true } });
  }
</script>

<div class="profileform">
  {#each specs as spec (spec.path)}
    {@const values = res.fields[spec.path] ?? []}
    {@const adds = pendingAdds(spec.path)}
    <div class="field">
      <svelte:element this={heading} class="fieldhead">{spec.label}</svelte:element>
      <ul class="vals">
        {#each values as fv, i (fv.node + i)}
          {@const removal = removalOf(spec.path, fv)}
          <li class="value" class:overridden={fv.overridden} class:pending-removed={!!removal}>
            <span class="v" class:iri={fv.iri}>{fv.v}</span>
            {#if fv.lang}<span class="lang">@{fv.lang}</span>{/if}
            <ProvenanceBadge prov={fv.prov} />
            {#if fv.overridden}<span class="ov-note">overridden</span>{/if}
            {#if removal}
              <span class="pend-note">removes on save</span>
              <button class="undo" onclick={() => onunstage(removal)} aria-label={"Undo removing " + fv.v}>
                ✕ undo
              </button>
            {:else if !fv.overridden}
              <button class="button button--quiet act" onclick={() => stageRemove(spec.path, fv)}>Remove</button>
            {/if}
          </li>
        {/each}
        {#each adds as p, i (i)}
          <li class="value pending-added">
            <span class="v" class:iri={p.value.iri}>{pickedLabels[p.value.v] ?? p.value.v}</span>
            {#if p.value.lang}<span class="lang">@{p.value.lang}</span>{/if}
            <span class="pend-note">adds on save</span>
            <button class="undo" onclick={() => onunstage(p.op)} aria-label={"Undo adding " + p.value.v}>
              ✕ undo
            </button>
          </li>
        {/each}
        {#if values.length === 0 && adds.length === 0}
          <li class="muted none">none</li>
        {/if}
      </ul>

      {#if spec.kind === "single"}
        <form
          class="addrow"
          onsubmit={(ev) => {
            ev.preventDefault();
            submitEntry(spec);
          }}
        >
          <input type="text" bind:value={entry[spec.path].v} aria-label={"New " + spec.label.toLowerCase()} placeholder={"New " + spec.label.toLowerCase() + "…"} />
          <button class="button button--quiet act" type="submit">Set</button>
          {#if values.length > 0}
            <button class="button button--quiet act" type="button" onclick={() => onstage({ resource, path: spec.path, action: "clear" })}>
              Clear
            </button>
          {/if}
        </form>
      {:else if spec.kind === "langLiteral"}
        <form
          class="addrow"
          onsubmit={(ev) => {
            ev.preventDefault();
            submitEntry(spec);
          }}
        >
          <input type="text" bind:value={entry[spec.path].v} aria-label={"New " + spec.label.toLowerCase()} placeholder={"Add a " + spec.label.toLowerCase() + "…"} />
          <input class="langbox" type="text" bind:value={entry[spec.path].lang} aria-label="Language tag" placeholder="lang (en)" />
          <button class="button button--quiet act" type="submit">Add</button>
        </form>
      {:else if spec.kind === "iri" || spec.kind === "literal"}
        <form
          class="addrow"
          onsubmit={(ev) => {
            ev.preventDefault();
            submitEntry(spec);
          }}
        >
          <input
            type="text"
            bind:value={entry[spec.path].v}
            aria-label={spec.label + (spec.kind === "iri" ? " IRI" : "")}
            placeholder={spec.hint ?? "Add a value…"}
            class:mono={spec.kind === "iri"}
          />
          <button class="button button--quiet act" type="submit">Add</button>
        </form>
      {:else if spec.kind === "vocab"}
        <button class="button button--quiet act" onclick={() => (pickerFor = spec.path)}>Add subject…</button>
      {:else if spec.kind === "tag"}
        <TagInput id={"tag-" + resource} label="Add a tag" placeholder="Type a tag…" onselect={(tag) => onstage({ resource, path: spec.path, action: "add", value: { v: tag } })} />
      {/if}
    </div>
  {/each}

  {#each extraFields as [path, values] (path)}
    <div class="field">
      <svelte:element this={heading} class="fieldhead">{prettify(path)}</svelte:element>
      <ul class="vals">
        {#each values as fv, i (fv.node + i)}
          <li class="value" class:overridden={fv.overridden}>
            <span class="v" class:iri={fv.iri}>{fv.v}</span>
            {#if fv.lang}<span class="lang">@{fv.lang}</span>{/if}
            <ProvenanceBadge prov={fv.prov} />
            {#if fv.overridden}<span class="ov-note">overridden</span>{/if}
          </li>
        {/each}
      </ul>
    </div>
  {/each}
</div>

{#if pickerFor}
  <VocabPicker title="Add a subject" onselect={subjectPicked} onclose={() => (pickerFor = null)} />
{/if}

<style>
  .field {
    margin: 0.9rem 0;
  }
  .fieldhead {
    margin: 0 0 0.25rem;
    font-size: 0.85rem;
    text-transform: uppercase;
    letter-spacing: 0.06em;
    color: var(--ink-muted);
  }
  .vals {
    margin: 0;
    padding: 0;
    list-style: none;
  }
  .value {
    display: flex;
    align-items: baseline;
    gap: 0.5rem;
    padding: 0.15rem 0;
    flex-wrap: wrap;
  }
  .value .v.iri {
    font-family: var(--mono);
    font-size: 0.9em;
  }
  .value.overridden .v,
  .value.pending-removed .v {
    text-decoration: line-through;
    color: var(--ink-muted);
  }
  .value.pending-removed,
  .value.pending-added {
    background: #fdf3dc;
    border: 1px dashed #ecd9a6;
    border-radius: 4px;
    padding: 0.15rem 0.4rem;
  }
  .pend-note {
    font-size: 0.72rem;
    font-weight: 600;
    color: #6b4d0c;
    letter-spacing: 0.03em;
  }
  .ov-note {
    font-size: 0.72rem;
    color: var(--danger);
    font-weight: 600;
  }
  .lang {
    color: var(--ink-muted);
    font-size: 0.8em;
  }
  .none {
    font-size: 0.85rem;
  }
  .act {
    font-size: 0.78rem;
    padding: 0.2em 0.7em;
  }
  .undo {
    background: none;
    border: 1px solid #ecd9a6;
    border-radius: 999px;
    color: #6b4d0c;
    font-size: 0.75rem;
    font-weight: 600;
    padding: 0.05em 0.6em;
  }
  .addrow {
    display: flex;
    gap: 0.4rem;
    align-items: center;
    margin-top: 0.3rem;
    flex-wrap: wrap;
  }
  .addrow input {
    min-width: 16rem;
  }
  .addrow input.mono {
    font-family: var(--mono);
    font-size: 0.85rem;
    min-width: 22rem;
  }
  .addrow .langbox {
    min-width: 6rem;
    width: 6rem;
  }
</style>
