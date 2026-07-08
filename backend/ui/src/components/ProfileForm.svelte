<script lang="ts">
  // Editable rendering of one resource's profile fields. The SPA knows the
  // shipped profile shapes, so each field maps to an add affordance and
  // per-value removal; the server owns the actual override semantics
  // (removing a feed value becomes an override there). Staged edits render
  // optimistically: pending values carry an undo, suppressed values strike
  // through before the save even lands.
  import ProvenanceBadge from "./ProvenanceBadge.svelte";
  import SearchSelect, { type SearchOption } from "./SearchSelect.svelte";
  import SubjectNeighborhood from "./SubjectNeighborhood.svelte";
  import TagInput from "./TagInput.svelte";
  import VocabPicker from "./VocabPicker.svelte";
  import { resolveTermURIs } from "../lib/api";
  import { bisacTerm, type BisacTerm } from "../lib/bisac";
  import { linkInfo } from "../lib/links";
  import { valueKey } from "../lib/ops";
  import { LANGUAGES, LANG_TAGS, languageTerm } from "../lib/languages";
  import { CARRIER_TYPES, CONTENT_TYPES, MEDIA_TYPES, rdaTerm, type RdaTerm } from "../lib/rdaterms";
  import { bestLabel } from "../lib/vocab";
  import type { FieldValue, Op, OpValue, ResourceDoc, Term } from "../lib/types";

  type FieldKind = "single" | "langLiteral" | "iri" | "vocab" | "tag" | "literal" | "readonly";

  interface FieldSpec {
    path: string;
    label: string;
    kind: FieldKind;
    hint?: string;
    /** Closed-list IRI fields (RDA media/carrier, MARC languages) get a
     *  searchable picker and labeled chips instead of raw URLs; the pinned
     *  "Other IRI…" keeps free entry available. */
    options?: SearchOption[];
    /** "more" fields fold into the default-collapsed "Additional details"
     *  disclosure (tasks/083) -- present, but not competing with the
     *  primary worksheet. */
    section?: "more";
    /** Prose fields (summary) span every worksheet column: paragraph text
     *  squeezed into one 30rem column reads worse than a full line. */
    wide?: boolean;
    /** Decodes a stored literal into a heading + code (BISAC), so coded
     *  values read like subjects with the raw code demoted. */
    decode?: (v: string) => BisacTerm | undefined;
  }

  /** Closed-list terms as searchable-picker entries. */
  function termOptions(terms: RdaTerm[]): SearchOption[] {
    return terms.map((t) => ({ value: t.iri, label: t.label, code: t.code, group: t.group }));
  }

  // The shipped work-monograph and instance-ebook shapes (profiles/defaults).
  // "readonly" fields surface values living inside typed blank structures
  // (contributions, notes, publication) -- rendered with provenance, edited
  // via the MARC tab until the op layer builds structures (tasks/083).
  const WORK_FIELDS: FieldSpec[] = [
    { path: "title", label: "Title", kind: "single" },
    { path: "subtitle", label: "Subtitle", kind: "single" },
    { path: "contributors", label: "Contributors", kind: "readonly" },
    { path: "summary", label: "Summary", kind: "langLiteral", wide: true },
    { path: "language", label: "Language", kind: "iri", options: termOptions(LANGUAGES) },
    { path: "subjects", label: "Subjects", kind: "vocab" },
    { path: "subjectLabels", label: "Subject headings", kind: "readonly" },
    { path: "tags", label: "Tags", kind: "tag" },
    { path: "genreForm", label: "Genre / form", kind: "readonly", section: "more" },
    { path: "content", label: "Content type", kind: "iri", options: termOptions(CONTENT_TYPES), section: "more" },
    { path: "classification", label: "Classification", kind: "readonly", section: "more", decode: bisacTerm },
  ];
  const INSTANCE_FIELDS: FieldSpec[] = [
    { path: "isbn", label: "Identifiers", kind: "literal", hint: "9780000000000" },
    { path: "media", label: "Media type", kind: "iri", options: termOptions(MEDIA_TYPES) },
    { path: "carrier", label: "Carrier type", kind: "iri", options: termOptions(CARRIER_TYPES) },
    { path: "links", label: "Links", kind: "iri", hint: "https://…" },
    { path: "responsibility", label: "Responsibility", kind: "single", section: "more" },
    { path: "edition", label: "Edition", kind: "single", section: "more" },
    { path: "publicationPlace", label: "Publication place", kind: "readonly", section: "more" },
    { path: "publisher", label: "Publisher", kind: "readonly", section: "more" },
    { path: "publicationDate", label: "Publication date", kind: "readonly", section: "more" },
    { path: "extent", label: "Extent", kind: "readonly", section: "more" },
    { path: "duration", label: "Duration", kind: "single", section: "more" },
    { path: "notes", label: "Notes", kind: "readonly", section: "more" },
    { path: "format", label: "Digital format", kind: "readonly", section: "more" },
    { path: "issuance", label: "Issuance", kind: "readonly", section: "more" },
  ];

  const CUSTOM_IRI = "__custom__";
  const OTHER_IRI: SearchOption[] = [{ value: CUSTOM_IRI, label: "Other IRI…" }];
  const LANG_TAG_OPTIONS: SearchOption[] = LANG_TAGS.map((lt) => ({ value: lt.tag, label: lt.label, code: lt.tag }));
  const LANG_TAG_PINNED: SearchOption[] = [
    { value: "", label: "no language tag" },
    { value: CUSTOM_IRI, label: "Other tag…" },
  ];

  /** The known closed-list term (RDA media/carrier or MARC language) for an
   *  IRI, so stored values render as names instead of URLs. */
  function iriTerm(v: string): RdaTerm | undefined {
    return rdaTerm(v) ?? languageTerm(v);
  }

  let {
    res,
    resource,
    kind,
    ops,
    onstage,
    onunstage,
    idKinds = {},
  }: {
    res: ResourceDoc;
    resource: string; // op resource: "work" or the instance id
    kind: "work" | "instance";
    ops: Op[]; // every staged op; filtered to this resource here
    onstage: (op: Op) => void;
    onunstage: (op: Op) => void;
    /** Identifier value -> BIBFRAME kind (isbn/issn/id) for badges (tasks/073). */
    idKinds?: Record<string, string>;
  } = $props();

  const ID_KIND_LABELS: Record<string, string> = { isbn: "ISBN", issn: "ISSN", id: "provider id" };

  // kind is fixed for a mounted form (work forms never become instances).
  // svelte-ignore state_referenced_locally
  const specs = kind === "work" ? WORK_FIELDS : INSTANCE_FIELDS;
  // svelte-ignore state_referenced_locally
  const heading = kind === "work" ? "h2" : "h4";
  const primarySpecs = specs.filter((s) => !s.section);
  const moreSpecs = specs.filter((s) => s.section === "more");
  const moreCount = $derived(moreSpecs.reduce((n, s) => n + (res.fields[s.path]?.length ?? 0), 0));

  let entry = $state(Object.fromEntries(specs.map((s) => [s.path, { v: "", lang: "", custom: "", langCustom: "" }])));
  let pickerFor = $state<string | null>(null);
  let pickedLabels = $state<Record<string, string>>({});
  // Vocabulary chips (tasks/071): stored URIs resolved to full terms so
  // subjects read as headings, with one expandable neighborhood at a time.
  let resolved = $state<Record<string, Term>>({});
  let expanded = $state<string | null>(null); // `${path}|${uri}`
  const attempted = new Set<string>();

  // Resolve every vocab-field URI once -- stored values and staged adds
  // alike (a subject staged from the lookup or a restored draft arrives as
  // a bare URI); unresolved URIs stay raw.
  $effect(() => {
    const missing: string[] = [];
    for (const spec of specs) {
      if (spec.kind !== "vocab") continue;
      for (const fv of res.fields[spec.path] ?? []) {
        if (fv.iri && !attempted.has(fv.v)) {
          attempted.add(fv.v);
          missing.push(fv.v);
        }
      }
      for (const p of pendingAdds(spec.path)) {
        if (p.value.iri && !attempted.has(p.value.v)) {
          attempted.add(p.value.v);
          missing.push(p.value.v);
        }
      }
    }
    if (missing.length === 0) return;
    resolveTermURIs(missing).then(
      (r) => (resolved = { ...resolved, ...r.terms }),
      () => {},
    );
  });

  function toggleExpand(path: string, uri: string): void {
    const key = path + "|" + uri;
    expanded = expanded === key ? null : key;
  }

  /** Neighborhood "Replace": remove the expanded subject, add the neighbor
   *  -- two ordinary staged ops, so preview/drafts/undo work unchanged. */
  function replaceSubject(path: string, fv: FieldValue, next: Term): void {
    pickedLabels[next.id] = bestLabel(next);
    stageRemove(path, fv);
    onstage({ resource, path, action: "add", value: { v: next.id, iri: true } });
    expanded = null;
  }

  /** Neighborhood "Add": the neighbor joins the subjects; the panel stays
   *  open so a cataloger can pull in several narrower terms in a row. */
  function addSubject(path: string, next: Term): void {
    pickedLabels[next.id] = bestLabel(next);
    onstage({ resource, path, action: "add", value: { v: next.id, iri: true } });
  }

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

  /** Splits an IRI into a muted host and its meaningful tail so vocabulary
   *  values read as terms, not URLs; the full IRI stays in the tooltip. A
   *  purely numeric tail keeps its parent segment (RDAMediaType/1003). */
  function iriParts(v: string): { host: string; tail: string } {
    try {
      const u = new URL(v);
      if (u.hash.length > 1) return { host: u.hostname, tail: u.hash.slice(1) };
      const segs = u.pathname.split("/").filter(Boolean);
      let tail = segs.pop() ?? "";
      if ((/^\d+$/.test(tail) || tail.length <= 2) && segs.length > 0) tail = segs.pop() + "/" + tail;
      return tail ? { host: u.hostname, tail } : { host: "", tail: v };
    } catch {
      return { host: "", tail: v };
    }
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
    const v = (box.v === CUSTOM_IRI ? box.custom : box.v).trim();
    if (!v || v === CUSTOM_IRI) return;
    const value: OpValue = { v };
    const lang = (box.lang === CUSTOM_IRI ? box.langCustom : box.lang).trim();
    if (spec.kind === "langLiteral" && lang && lang !== CUSTOM_IRI) value.lang = lang;
    if (spec.kind === "iri") value.iri = true;
    if (spec.kind === "single") onstage({ resource, path: spec.path, action: "set", values: [value] });
    else onstage({ resource, path: spec.path, action: "add", value });
    box.v = "";
    box.custom = "";
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
  {#snippet fieldBlock(spec: FieldSpec)}
    {@const values = res.fields[spec.path] ?? []}
    {@const adds = pendingAdds(spec.path)}
    <div class="field" class:wide={spec.wide}>
      <svelte:element this={heading} class="fieldhead">{spec.label}</svelte:element>
      <ul class="vals">
        {#each values as fv, i (fv.node + i)}
          {@const removal = removalOf(spec.path, fv)}
          {@const term = spec.kind === "vocab" && fv.iri ? resolved[fv.v] : undefined}
          {@const expKey = spec.path + "|" + fv.v}
          <li class="value" class:overridden={fv.overridden} class:pending-removed={!!removal}>
            {#if term}
              <button
                class="chip"
                class:open={expanded === expKey}
                title={fv.v}
                aria-expanded={expanded === expKey}
                onclick={() => toggleExpand(spec.path, fv.v)}
              >
                <span class="v chip-label">{bestLabel(term)}</span>
                <span class="chip-scheme">{term.scheme}</span>
                <span class="chip-caret" aria-hidden="true">{expanded === expKey ? "▾" : "▸"}</span>
              </button>
            {:else if fv.iri && spec.kind === "vocab" && fv.annotation}
              {@const p = iriParts(fv.v)}
              <!-- Vocab-index miss, but the grain carries the term's own
                   skos:prefLabel (tasks/137): show the name; the hint still
                   signals no browse/hierarchy/typeahead for this term. -->
              <span class="v" title={fv.v}>{fv.annotation}</span>
              {#if p.host}<span class="chip-scheme" title={fv.v}>{p.host}</span>{/if}
              <span class="unres muted">not in local index</span>
            {:else if fv.iri && iriTerm(fv.v)}
              {@const rt = iriTerm(fv.v)!}
              <span class="v" title={fv.v}>{rt.label}</span>
              <span class="rdacode" title={fv.v}>{rt.code}</span>
            {:else if fv.iri && spec.path === "links" && /^https?:\/\//.test(fv.v)}
              <!-- The locator's grain-carried 856 $3 label (rdfs:label
                   annotation, libcodex v0.15.0 / tasks/147) wins; the
                   URL-shape heuristic covers label-less locators and
                   pre-0.15 grains. -->
              {@const li = linkInfo(fv.v)}
              {@const label = fv.annotation || li.label}
              {@const p = iriParts(fv.v)}
              <a class="v linkval" href={fv.v} target="_blank" rel="noreferrer" title={fv.v}>
                {#if li.image}
                  <img class="linkthumb" src={fv.v} alt={label || "linked image"} loading="lazy" />
                {/if}
                {#if label}
                  <span class="linklabel">{label}</span>
                  <span class="iri linkhost">{p.host}</span>
                {:else}
                  <span class="iri">{#if p.host}<span class="iri-host">{p.host}</span>{/if}{p.tail}</span>
                {/if}
              </a>
            {:else if fv.iri && /^https?:\/\//.test(fv.v)}
              {@const p = iriParts(fv.v)}
              <a class="v iri" href={fv.v} target="_blank" rel="noreferrer" title={fv.v}>
                {#if p.host}<span class="iri-host">{p.host}</span>{/if}{p.tail}
              </a>
              {#if spec.kind === "vocab"}<span class="unres muted">not in local index</span>{/if}
            {:else if fv.iri}
              {@const p = iriParts(fv.v)}
              <span class="v iri" title={fv.v}>
                {#if p.host}<span class="iri-host">{p.host}</span>{/if}{p.tail}
              </span>
              {#if spec.kind === "vocab"}<span class="unres muted">not in local index</span>{/if}
            {:else if spec.decode?.(fv.v)}
              {@const dt = spec.decode(fv.v)!}
              <span class="v" title={fv.v}>{dt.label}</span>
              {#if !dt.exact}<span class="rdacode" title={fv.v}>{dt.code}</span>{/if}
            {:else}
              <span class="v">{fv.v}</span>
            {/if}
            {#if spec.kind === "literal" && idKinds[fv.v]}
              <span class="idkind">{ID_KIND_LABELS[idKinds[fv.v]] ?? idKinds[fv.v]}</span>
            {/if}
            {#if fv.annotation && spec.kind !== "vocab"}
              {#if spec.path === "contributors"}
                <!-- The contribution's bf:role label (tasks/138), presented
                     like the public site's "Name (role)". -->
                <span class="muted">({fv.annotation})</span>
              {:else}
                <span class="chip-scheme" title={"heading source: " + fv.annotation}>{fv.annotation}</span>
              {/if}
            {/if}
            {#if fv.lang}<span class="lang">@{fv.lang}</span>{/if}
            <ProvenanceBadge prov={fv.prov} />
            {#if fv.overridden}<span class="ov-note">overridden</span>{/if}
            {#if removal}
              <span class="pend-note">removes on save</span>
              <button class="undo" onclick={() => onunstage(removal)} aria-label={"Undo removing " + fv.v}>
                ✕ undo
              </button>
            {:else if !fv.overridden && spec.kind !== "readonly"}
              <button class="button button--quiet act" onclick={() => stageRemove(spec.path, fv)}>Remove</button>
            {/if}
          </li>
          {#if term && expanded === expKey && !removal}
            <li class="hoodrow">
              <SubjectNeighborhood
                {term}
                onreplace={(t) => replaceSubject(spec.path, fv, t)}
                onadd={(t) => addSubject(spec.path, t)}
              />
            </li>
          {/if}
        {/each}
        {#each adds as p, i (i)}
          <li class="value pending-added">
            {#if p.value.iri && iriTerm(p.value.v)}
              {@const rt = iriTerm(p.value.v)!}
              <span class="v" title={p.value.v}>{rt.label}</span>
              <span class="rdacode" title={p.value.v}>{rt.code}</span>
            {:else if p.value.iri && resolved[p.value.v]}
              {@const t = resolved[p.value.v]}
              <span class="v" title={p.value.v}>{bestLabel(t)}</span>
              <span class="chip-scheme">{t.scheme}</span>
            {:else if p.value.iri && pickedLabels[p.value.v]}
              <span class="v" title={p.value.v}>{pickedLabels[p.value.v]}</span>
            {:else if p.value.iri}
              {@const ip = iriParts(p.value.v)}
              <span class="v iri" title={p.value.v}>
                {#if ip.host}<span class="iri-host">{ip.host}</span>{/if}{ip.tail}
              </span>
            {:else}
              <span class="v">{p.value.v}</span>
            {/if}
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
          <div class="langpick">
            <SearchSelect options={LANG_TAG_OPTIONS} pinned={LANG_TAG_PINNED} bind:value={entry[spec.path].lang} ariaLabel="Language tag" placeholder="no language tag" />
          </div>
          {#if entry[spec.path].lang === CUSTOM_IRI}
            <input class="langbox" type="text" bind:value={entry[spec.path].langCustom} aria-label="Custom language tag" placeholder="tag (pt-BR)" />
          {/if}
          <button class="button button--quiet act" type="submit">Add</button>
        </form>
      {:else if spec.options}
        <form
          class="addrow"
          onsubmit={(ev) => {
            ev.preventDefault();
            submitEntry(spec);
          }}
        >
          <SearchSelect
            options={spec.options}
            pinned={OTHER_IRI}
            bind:value={entry[spec.path].v}
            ariaLabel={"Add a " + spec.label.toLowerCase()}
            placeholder={"Add a " + spec.label.toLowerCase() + "…"}
          />
          {#if entry[spec.path].v === CUSTOM_IRI}
            <input type="text" class="mono" bind:value={entry[spec.path].custom} aria-label={spec.label + " IRI"} placeholder="http://…" />
          {/if}
          <button class="button button--quiet act" type="submit" disabled={!entry[spec.path].v || (entry[spec.path].v === CUSTOM_IRI && !entry[spec.path].custom.trim())}>
            Add
          </button>
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
        <TagInput id={"tag-" + resource} label="Add a tag" hideLabel placeholder="Type a tag…" onselect={(tag) => onstage({ resource, path: spec.path, action: "add", value: { v: tag } })} />
      {/if}
    </div>
  {/snippet}

  {#each primarySpecs as spec (spec.path)}
    {@render fieldBlock(spec)}
  {/each}

  {#if moreSpecs.length > 0}
    <details class="morefields">
      <summary>Additional details{#if moreCount > 0}&nbsp;({moreCount}){/if}</summary>
      <div class="moregrid">
        {#each moreSpecs as spec (spec.path)}
          {@render fieldBlock(spec)}
        {/each}
      </div>
    </details>
  {/if}

  {#each extraFields as [path, values] (path)}
    <div class="field">
      <svelte:element this={heading} class="fieldhead">{prettify(path)}</svelte:element>
      <ul class="vals">
        {#each values as fv, i (fv.node + i)}
          <li class="value" class:overridden={fv.overridden}>
            {#if fv.iri}
              {@const p = iriParts(fv.v)}
              <span class="v iri" title={fv.v}>
                {#if p.host}<span class="iri-host">{p.host}</span>{/if}{p.tail}
              </span>
            {:else}
              <span class="v">{fv.v}</span>
            {/if}
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
  /* The ruled worksheet: drawer-label column on the left, asserted values on
     the right, one hairline per field. Labels sit on the first value's
     baseline so the record scans as rows, not stacked blocks. On wide
     screens the field blocks flow into columns (DOM order = focus order). */
  .profileform {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(30rem, 1fr));
    gap: 0 2.5rem;
    align-items: start;
  }
  .field {
    display: grid;
    grid-template-columns: 9.5rem 1fr;
    gap: 0.35rem 1.25rem;
    padding: 0.55rem 0;
    border-top: 1px solid var(--rule);
    margin: 0;
  }
  .fieldhead {
    margin: 0;
    padding-top: 0.3rem;
    font-size: 0.72rem;
    font-weight: 650;
    text-transform: uppercase;
    letter-spacing: 0.07em;
    color: var(--ink-muted);
  }
  .field > :global(*) {
    grid-column: 2;
    justify-self: start;
  }
  .field > .fieldhead {
    grid-column: 1;
  }
  .field > .vals,
  .field > .addrow {
    justify-self: stretch;
  }
  /* Prose fields (summary) span the whole worksheet; the entry box grows to
     the full line so paragraph text is written where it will be read. */
  .field.wide {
    grid-column: 1 / -1;
  }
  .field.wide .addrow input[type="text"]:first-child {
    flex: 1 1 16rem;
  }
  .vals {
    margin: 0;
    padding: 0;
    list-style: none;
  }
  /* tasks/083: secondary fields fold under one full-width disclosure; the
     summary reads like a field label so the closed state stays quiet. */
  .morefields {
    grid-column: 1 / -1;
    margin: 0;
    padding: 0.55rem 0 0;
    border-top: 1px solid var(--rule);
  }
  .morefields summary {
    cursor: pointer;
    font-size: 0.72rem;
    font-weight: 650;
    text-transform: uppercase;
    letter-spacing: 0.07em;
    color: var(--ink-muted);
  }
  .moregrid {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(30rem, 1fr));
    gap: 0 2.5rem;
    align-items: start;
    margin-top: 0.35rem;
  }
  a.v.iri {
    color: inherit;
    text-decoration-color: var(--ink-muted);
  }
  a.v.iri:hover {
    color: var(--accent);
  }
  @media (max-width: 40rem) {
    .field {
      grid-template-columns: 1fr;
    }
    .field > :global(*) {
      grid-column: 1;
    }
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
  /* tasks/071: a resolved vocabulary value reads as a heading chip; the
     caret discloses its SKOS neighborhood inline. */
  .chip {
    display: inline-flex;
    align-items: baseline;
    gap: 0.45rem;
    background: var(--surface);
    border: 1px solid var(--rule);
    border-radius: 999px;
    padding: 0.1em 0.75em;
    color: inherit;
    cursor: pointer;
    text-align: left;
  }
  .chip:hover,
  .chip.open {
    border-color: var(--accent);
  }
  .chip-label {
    font-weight: 600;
  }
  .chip-scheme {
    font-size: 0.68rem;
    font-weight: 650;
    text-transform: uppercase;
    letter-spacing: 0.05em;
    color: var(--ink-muted);
  }
  .chip-caret {
    font-size: 0.7rem;
    color: var(--ink-muted);
  }
  .value.pending-removed .chip .chip-label,
  .value.overridden .chip .chip-label {
    text-decoration: line-through;
    color: var(--ink-muted);
  }
  .unres {
    font-size: 0.72rem;
  }
  .idkind {
    font-size: 0.66rem;
    font-weight: 650;
    text-transform: uppercase;
    letter-spacing: 0.05em;
    color: var(--ink-muted);
    border: 1px solid var(--rule);
    border-radius: 999px;
    padding: 0.05em 0.55em;
  }
  /* An RDA media/carrier value reads as its label; the MARC code rides
     along muted, the IRI stays in the tooltip. */
  .rdacode {
    font-family: var(--mono);
    font-size: 0.72rem;
    color: var(--ink-muted);
    border: 1px solid var(--rule);
    border-radius: 999px;
    padding: 0.02em 0.5em;
  }
  .hoodrow {
    list-style: none;
    padding: 0;
  }
  .iri-host {
    color: var(--ink-muted);
    font-size: 0.85em;
  }
  .iri-host::after {
    content: " › ";
  }
  .linkval {
    display: inline-flex;
    align-items: center;
    gap: 0.5rem;
    text-decoration: none;
  }
  .linkval:hover .linklabel {
    text-decoration: underline;
  }
  .linkthumb {
    display: block;
    height: 3.4rem;
    width: auto;
    max-width: 6rem;
    object-fit: cover;
    border: 1px solid var(--rule);
    border-radius: 3px;
    background: var(--surface-2, transparent);
  }
  .linklabel {
    font-weight: 600;
  }
  .linkhost {
    color: var(--ink-muted);
    font-size: 0.8em;
  }
  .value.overridden .v,
  .value.pending-removed .v {
    text-decoration: line-through;
    color: var(--ink-muted);
  }
  .value.pending-removed,
  .value.pending-added {
    background: var(--pend-bg);
    border: 1px dashed var(--pend-edge);
    border-radius: var(--radius);
    padding: 0.15rem 0.4rem;
  }
  .pend-note {
    font-size: 0.72rem;
    font-weight: 600;
    color: var(--pend-ink);
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
    padding: 0.1em 0.7em;
  }
  .undo {
    background: none;
    border: 1px solid var(--pend-edge);
    border-radius: 999px;
    color: var(--pend-ink);
    font-size: 0.75rem;
    font-weight: 600;
    padding: 0.05em 0.6em;
  }
  /* Entry rows are subordinate to asserted values: shorter controls, quieter
     text, no competing with the record itself. */
  .addrow {
    display: flex;
    gap: 0.4rem;
    align-items: center;
    margin-top: 0.15rem;
    flex-wrap: wrap;
  }
  .addrow input,
  .addrow :global(.searchselect input),
  .addrow :global(.button) {
    min-height: 1.8rem;
    font-size: 0.85rem;
  }
  .addrow input {
    min-width: 16rem;
  }
  .addrow input.mono {
    font-family: var(--mono);
    font-size: 0.8rem;
    min-width: 22rem;
  }
  .addrow input.langbox {
    min-width: 6rem;
    width: 6rem;
  }
  /* Term pickers get room for long labels ("Creoles and Pidgins, …"); the
     tag picker stays subordinate to the literal box beside it. */
  .addrow > :global(.searchselect) {
    min-width: 18rem;
  }
  .langpick :global(.searchselect) {
    min-width: 11rem;
  }
</style>
