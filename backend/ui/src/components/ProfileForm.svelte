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
  import { linkInfo } from "../lib/links";
  import { valueKey } from "../lib/ops";
  import { presentIRIs, wouldChange } from "../lib/subjects";
  import { LANG_TAGS, languageTerm } from "../lib/languages";
  import { rdaTerm, type RdaTerm } from "../lib/rdaterms";
  import { bestLabel } from "../lib/vocab";
  import { INSTANCE_FIELDS, WORK_FIELDS, buildFieldSpecs, type FieldSpec } from "./profileSpecs";
  import type { FieldValue, Op, OpValue, ProfileField, ResourceDoc, Term } from "../lib/types";


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
    fields,
  }: {
    res: ResourceDoc;
    resource: string; // op resource: "work" or the instance id
    kind: "work" | "instance";
    ops: Op[]; // every staged op; filtered to this resource here
    onstage: (op: Op) => void;
    onunstage: (op: Op) => void;
    /** Identifier value -> BIBFRAME kind (isbn/issn/id) for badges. */
    idKinds?: Record<string, string>;
    /** The resource's editing profile fields: when present, the form
     *  takes its field set, order, labels, and hidden flags from the deployment's
     *  profile -- the headline promise of the profile mechanism. The presentation
     *  concerns the server has no vocabulary for (kind, options, decode, section,
     *  wide) stay in the local table below, merged by path. Absent (a caller that
     *  has not fetched a profile) falls back to the shipped default shape. */
    fields?: ProfileField[];
  } = $props();

  const ID_KIND_LABELS: Record<string, string> = { isbn: "ISBN", issn: "ISSN", id: "provider id" };

  // The record editor obeys its editing profile: the profile owns the
  // field set, order, labels, and hidden flags; the local presentation table owns
  // kind/options/decode/section/wide, merged by path in buildFieldSpecs. Absent a
  // profile the shipped shape stands. The mount is stable (the caller renders this
  // form only once the profile has resolved), so this is a mount-time const.
  // svelte-ignore state_referenced_locally
  const specs: FieldSpec[] = buildFieldSpecs(kind === "work" ? WORK_FIELDS : INSTANCE_FIELDS, fields);
  // svelte-ignore state_referenced_locally
  const heading = kind === "work" ? "h2" : "h4";
  // Subjects and tags render side by side in one full-width row;
  // everything else flows the two-column worksheet as before.
  const pairedPaths = new Set(["subjects", "tags"]);
  const pairedSpecs = specs.filter((s) => !s.section && pairedPaths.has(s.path));
  const primarySpecs = specs.filter((s) => !s.section && !(pairedSpecs.length === 2 && pairedPaths.has(s.path)));
  const moreSpecs = specs.filter((s) => s.section === "more");
  const moreCount = $derived(moreSpecs.reduce((n, s) => n + (res.fields[s.path]?.length ?? 0), 0));

  let entry = $state(Object.fromEntries(specs.map((s) => [s.path, { v: "", lang: "", custom: "", langCustom: "" }])));
  let pickerFor = $state<string | null>(null);
  let pickedLabels = $state<Record<string, string>>({});
  // Vocabulary chips: stored URIs resolved to full terms so
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

  // every resolved term's broader parents resolve too, one
  // batched call per level (breadth-first; `attempted` bounds the calls and
  // breaks vocabulary cycles), so SKOS chips can show a root-first path.
  $effect(() => {
    const missing: string[] = [];
    for (const t of Object.values(resolved)) {
      for (const b of t.broader ?? []) {
        if (!attempted.has(b)) {
          attempted.add(b);
          missing.push(b);
        }
      }
    }
    if (missing.length === 0) return;
    resolveTermURIs(missing).then(
      (r) => (resolved = { ...resolved, ...r.terms }),
      () => {},
    );
  });

  // Scheme -> whether any resolved term of it carries hierarchy links:
  // SKOS-shaped vocabularies group by root with a path on each chip, flat
  // ones (FAST) stack in compact columns -- the same distinction the
  // work-search rail draws.
  const schemeHierarchy = $derived.by(() => {
    const h: Record<string, boolean> = {};
    for (const t of Object.values(resolved)) {
      if (t.scheme && ((t.broader?.length ?? 0) > 0 || (t.narrower?.length ?? 0) > 0)) h[t.scheme] = true;
    }
    return h;
  });

  interface MergedValue {
    fv: FieldValue;
    provs: string[];
  }

  /** Collapses identical values asserted by several feeds -- a multi-feed
   *  cluster carries one statement per graph -- into one display row wearing
   * every provenance badge. Ops key on the value, not the
   *  graph, so acting on the merged row acts on every assertion. */
  function mergeProv(values: FieldValue[]): MergedValue[] {
    const out: MergedValue[] = [];
    const idx = new Map<string, MergedValue>();
    for (const fv of values) {
      const key =
        valueKey({ v: fv.v, lang: fv.lang, iri: fv.iri }) + "|" + (fv.annotation ?? "") + "|" + (fv.overridden ? "o" : "");
      const m = idx.get(key);
      if (m) {
        if (!m.provs.includes(fv.prov)) m.provs.push(fv.prov);
        continue;
      }
      const nm = { fv, provs: [fv.prov] };
      idx.set(key, nm);
      out.push(nm);
    }
    return out;
  }

  interface VocabEntry {
    fv: FieldValue;
    provs: string[];
    pathPrefix: string;
  }
  interface SkosGroup {
    rootId: string;
    rootLabel: string;
    scheme: string;
    entries: VocabEntry[];
  }
  interface FlatGroup {
    scheme: string;
    entries: VocabEntry[];
  }

  /** Root-first broader chain ending at the term itself: first parent when
   *  a term has several, cycle-safe, depth-capped like the projector. */
  function ancestorChain(term: Term): Term[] {
    const out = [term];
    const seen = new Set([term.id]);
    let cur = term;
    for (let d = 0; d < 12; d++) {
      const up = (cur.broader ?? []).map((b) => resolved[b]).find(Boolean);
      if (!up || seen.has(up.id)) break;
      seen.add(up.id);
      out.unshift(up);
      cur = up;
    }
    return out;
  }

  /** Groups a vocab field's stored values for display: SKOS
   *  terms by their root ancestor (chips carry the intermediate path), flat
   *  schemes' terms per scheme for the column stacks, unresolved values
   *  left to the generic list. Entries sort by label for scanability. */
  function subjectGroups(values: FieldValue[]): { skos: SkosGroup[]; flat: FlatGroup[]; rest: MergedValue[] } {
    const skos = new Map<string, SkosGroup>();
    const flat = new Map<string, FlatGroup>();
    const rest: MergedValue[] = [];
    for (const m of mergeProv(values)) {
      const { fv, provs } = m;
      const term = fv.iri ? resolved[fv.v] : undefined;
      if (!term) {
        rest.push(m);
        continue;
      }
      if (schemeHierarchy[term.scheme ?? ""]) {
        const c = ancestorChain(term);
        const root = c[0];
        const g = skos.get(root.id) ?? { rootId: root.id, rootLabel: bestLabel(root), scheme: term.scheme ?? "", entries: [] };
        g.entries.push({ fv, provs, pathPrefix: c.slice(1, -1).map((t) => bestLabel(t)).join(" › ") });
        skos.set(root.id, g);
      } else {
        const key = term.scheme ?? "";
        const g = flat.get(key) ?? { scheme: key, entries: [] };
        g.entries.push({ fv, provs, pathPrefix: "" });
        flat.set(key, g);
      }
    }
    const label = (e: VocabEntry) => bestLabel(resolved[e.fv.v]);
    for (const g of skos.values()) g.entries.sort((a, b) => label(a).localeCompare(label(b)));
    for (const g of flat.values()) g.entries.sort((a, b) => label(a).localeCompare(label(b)));
    return {
      skos: [...skos.values()].sort((a, b) => a.rootLabel.localeCompare(b.rootLabel)),
      flat: [...flat.values()].sort((a, b) => a.scheme.localeCompare(b.scheme)),
      rest,
    };
  }

  function toggleExpand(path: string, uri: string): void {
    const key = path + "|" + uri;
    expanded = expanded === key ? null : key;
  }

  /** The IRIs a field currently holds, after staged removals -- what the
   *  neighborhood needs in order not to offer Add for a term already there. */
  function currentIRIs(path: string): Set<string> {
    return presentIRIs(res.fields[path] ?? [], fieldOps(path));
  }

  /** Neighborhood "Replace": remove the expanded subject, add the neighbor
   *  -- two ordinary staged ops, so preview/drafts/undo work unchanged.
   *  Replacing with a term the record already carries is just the removal:
   * the add would stage an edit that changes nothing. */
  function replaceSubject(path: string, fv: FieldValue, next: Term): void {
    // Read the field's state before staging the removal: the ops prop updates
    // through the store, not within this call.
    const present = currentIRIs(path);
    pickedLabels[next.id] = bestLabel(next);
    stageRemove(path, fv);
    if (wouldChange(next.id, present)) {
      onstage({ resource, path, action: "add", value: { v: next.id, iri: true } });
    }
    expanded = null;
  }

  /** Neighborhood "Add": the neighbor joins the subjects; the panel stays
   *  open so a cataloger can pull in several narrower terms in a row. A term
   *  the record already carries is not added: the button for it is disabled,
   * and this is the guard behind the button. */
  function addSubject(path: string, next: Term): void {
    if (!wouldChange(next.id, currentIRIs(path))) return;
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
  {#snippet valueRow(spec: FieldSpec, fv: FieldValue, pathPrefix: string, provs: string[])}
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
          <span class="v chip-label">
            {#if pathPrefix}<span class="pathpre">{pathPrefix} › </span>{/if}{bestLabel(term)}
          </span>
          <span class="chip-scheme">{term.scheme}</span>
          <span class="chip-caret" aria-hidden="true">{expanded === expKey ? "▾" : "▸"}</span>
        </button>
      {:else if fv.iri && spec.kind === "vocab" && fv.annotation}
              {@const p = iriParts(fv.v)}
              <!-- Vocab-index miss, but the grain carries the term's own
                   skos:prefLabel: show the name; the hint still
                   signals no browse/hierarchy/typeahead for this term. -->
              <span class="v" title={fv.v}>{fv.annotation}</span>
              {#if p.host}<span class="chip-scheme" title={fv.v}>{p.host}</span>{/if}
              <span class="unres muted">not in local index</span>
            {:else if fv.iri && iriTerm(fv.v)}
              {@const rt = iriTerm(fv.v)!}
              <span class="v" title={fv.v}>{rt.label}</span>
              <span class="rdacode" title={fv.v}>{rt.code}</span>
            {:else if fv.iri && spec.path === "links" && /^https?:\/\//.test(fv.v)}
              <!-- The locator's grain-carried 856 $3 label (an rdfs:label
                   annotation since libcodex v0.15.0) wins; the
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
                <!-- The contribution's bf:role label, presented
                     like the public site's "Name (role)". -->
                <span class="muted">({fv.annotation})</span>
              {:else}
                <span class="chip-scheme" title={"heading source: " + fv.annotation}>{fv.annotation}</span>
              {/if}
            {/if}
            {#if fv.lang}<span class="lang">@{fv.lang}</span>{/if}
            {#each provs as p (p)}
              <ProvenanceBadge prov={p} />
            {/each}
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
          present={currentIRIs(spec.path)}
          onreplace={(t) => replaceSubject(spec.path, fv, t)}
          onadd={(t) => addSubject(spec.path, t)}
        />
      </li>
    {/if}
  {/snippet}

  {#snippet pendingRow(p: PendingValue)}
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
  {/snippet}

  {#snippet fieldBlock(spec: FieldSpec)}
    {@const values = res.fields[spec.path] ?? []}
    {@const adds = pendingAdds(spec.path)}
    <div class="field" class:wide={spec.wide}>
      <svelte:element this={heading} class="fieldhead">{spec.label}</svelte:element>
      {#if spec.kind === "vocab" && values.length > 0}
        <!-- SKOS terms group under their root ancestor with the
             intermediate path on each chip; flat schemes (FAST) stack in
             columns; unresolved values fall through to the plain list. -->
        {@const g = subjectGroups(values)}
        {#each g.skos as grp (grp.rootId)}
          <div class="vgroup">
            <div class="vgrouphead">
              <span class="vgroupname">{grp.rootLabel}</span>
              <span class="chip-scheme">{grp.scheme}</span>
            </div>
            <ul class="vals">
              {#each grp.entries as e, i (e.fv.node + i)}
                {@render valueRow(spec, e.fv, e.pathPrefix, e.provs)}
              {/each}
            </ul>
          </div>
        {/each}
        {#each g.flat as grp (grp.scheme)}
          <div class="vgroup">
            <div class="vgrouphead"><span class="chip-scheme">{grp.scheme || "other"}</span></div>
            <ul class="vals flatcols">
              {#each grp.entries as e, i (e.fv.node + i)}
                {@render valueRow(spec, e.fv, "", e.provs)}
              {/each}
            </ul>
          </div>
        {/each}
        {#if g.rest.length > 0 || adds.length > 0}
          <ul class="vals">
            {#each g.rest as m, i (m.fv.node + i)}
              {@render valueRow(spec, m.fv, "", m.provs)}
            {/each}
            {#each adds as p, i (i)}
              {@render pendingRow(p)}
            {/each}
          </ul>
        {/if}
      {:else}
        <ul class="vals">
          {#each mergeProv(values) as m, i (m.fv.node + i)}
            {@render valueRow(spec, m.fv, "", m.provs)}
          {/each}
          {#each adds as p, i (i)}
            {@render pendingRow(p)}
          {/each}
          {#if values.length === 0 && adds.length === 0}
            <li class="muted none">none</li>
          {/if}
        </ul>
      {/if}

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
        <button class="button button--quiet act" onclick={() => (pickerFor = spec.path)}>
          {spec.path === "subjects" ? "Add subject…" : `Look up ${spec.label.toLowerCase()}…`}
        </button>
      {:else if spec.kind === "tag"}
        <TagInput id={"tag-" + resource} label="Add a tag" hideLabel placeholder="Type a tag…" onselect={(tag) => onstage({ resource, path: spec.path, action: "add", value: { v: tag } })} />
      {/if}
    </div>
  {/snippet}

  {#each primarySpecs as spec (spec.path)}
    {@render fieldBlock(spec)}
  {/each}

  {#if pairedSpecs.length === 2}
    <div class="pairrow">
      {#each pairedSpecs as spec (spec.path)}
        {@render fieldBlock(spec)}
      {/each}
    </div>
  {/if}

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
        {#each mergeProv(values) as m, i (m.fv.node + i)}
          <li class="value" class:overridden={m.fv.overridden}>
            {#if m.fv.iri}
              {@const p = iriParts(m.fv.v)}
              <span class="v iri" title={m.fv.v}>
                {#if p.host}<span class="iri-host">{p.host}</span>{/if}{p.tail}
              </span>
            {:else}
              <span class="v">{m.fv.v}</span>
            {/if}
            {#if m.fv.lang}<span class="lang">@{m.fv.lang}</span>{/if}
            {#each m.provs as p (p)}
              <ProvenanceBadge prov={p} />
            {/each}
            {#if m.fv.overridden}<span class="ov-note">overridden</span>{/if}
          </li>
        {/each}
      </ul>
    </div>
  {/each}
</div>

{#if pickerFor}
  {@const pickerSpec = specs.find((s) => s.path === pickerFor)}
  <VocabPicker
    title={pickerFor === "subjects" ? "Add a subject" : `Add: ${pickerSpec?.label ?? "term"}`}
    initialSource={pickerSpec?.vocabRef}
    onselect={subjectPicked}
    onclose={() => (pickerFor = null)}
  />
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
  /* subjects and tags share one full-width row -- subjects takes
     the flexible width for its vocabulary groups, tags a fixed rail. */
  .pairrow {
    grid-column: 1 / -1;
    display: grid;
    grid-template-columns: minmax(0, 1fr) minmax(16rem, 24rem);
    gap: 0 2.5rem;
    align-items: start;
  }
  @media (max-width: 52rem) {
    .pairrow {
      grid-template-columns: 1fr;
    }
  }
  /* vocabulary display groups. A SKOS group is headed by its
     root term; a flat scheme (FAST) stacks its chips in columns. */
  .field > .vgroup {
    justify-self: stretch;
  }
  .vgroup {
    margin: 0.1rem 0 0.45rem;
  }
  .vgrouphead {
    display: flex;
    align-items: baseline;
    gap: 0.5rem;
    margin-bottom: 0.1rem;
  }
  .vgroupname {
    font-size: 0.78rem;
    font-weight: 650;
    color: var(--ink-muted);
  }
  /* Wide enough that a typical heading chip + scheme badge + provenance +
     Remove sit on ONE line; 17rem wrapped the controls under most chips,
     making every FAST subject a ragged two-line item. */
  .vals.flatcols {
    column-width: 21rem;
    column-gap: 1.5rem;
  }
  .vals.flatcols > :global(li) {
    break-inside: avoid;
    /* A genuinely long heading still wraps its controls; keep the wrapped
       line snug under the chip so the item reads as one unit. */
    row-gap: 0;
  }
  .vals.flatcols > :global(li.hoodrow) {
    column-span: all;
  }
  .pathpre {
    font-weight: 400;
    color: var(--ink-muted);
  }
  .field.wide .addrow input[type="text"]:first-child {
    flex: 1 1 16rem;
  }
  .vals {
    margin: 0;
    padding: 0;
    list-style: none;
  }
  /* secondary fields fold under one full-width disclosure; the
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
  /* a resolved vocabulary value reads as a heading chip; the
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
