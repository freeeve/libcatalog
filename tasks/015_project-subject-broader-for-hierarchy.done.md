# 014 -- Project subject `broader` so consumers can render vocabulary hierarchy

> Filed by the qllpoc session as a cross-repo handoff (uncommitted, per the repo
> boundary). Follow-on to `tasks/012` (controlled subjects + labels).

## Problem

`tasks/012` gave controlled subjects a stable id + resolved labels, and the
projector's `buildLabelIndex` already reads authority statements off the subject
URI across every graph. But `catalog.json`'s `Subject` (`project/project.go`
~L399 `subjectsAndTags`, L409 `Subject{ID, Labels}`) carries **only id + labels**
-- not the `skos:broader` links that authority records supply. So a consumer
cannot render vocabulary **hierarchy** (breadcrumb trails, "narrower/broader"
facet drill-down) without re-reading the graph itself, which defeats the
projected-JSON contract.

Concrete driver: qllpoc materializes a Homosaurus authority graph
(`authority:homosaurus`) with `skos:prefLabel`/`skos:altLabel`/**`skos:broader`**
per term (its `cmd/graphauthorities`), and has a signature "subject trails"
feature (broader hops). Today it can't drive that from `catalog.json`.

## Ask

1. **Index `skos:broader` alongside labels.** Extend the authority index
   (`buildLabelIndex`, ~L440) to also capture each term URI's broader term URIs.
2. **Emit `broader` on `Subject`.** Add `Broader []string` (the parent term URIs)
   to the projected `Subject` (and, if useful, to `SubjectFacet`). Keep it id-only
   -- the parent's own label is already resolvable because it is (or should be) its
   own `Subject`/authority record; a consumer joins by id.
3. **Optionally `altLabel`.** The same authority records carry `skos:altLabel`;
   exposing `AltLabels map[string][]string` would let consumers use them as search
   synonyms. Lower priority than `broader`; include if cheap.
4. **Schema version bump** (v4 -> v5); the adapter already fails loudly on
   mismatch, so consumers reproject.

## Not in scope

- Rendering the hierarchy (that's the consuming site/module's job).
- `qll:`-style deployment extension fields (e.g. list membership) -- a separate
  question about whether/how the projector surfaces non-BIBFRAME predicates; not
  this task.

## Acceptance

- [x] `catalog.json` (v5) `Subject` carries `broader` term URIs when the graph has
  `skos:broader` for that subject; empty/omitted otherwise.
- [x] Provider/vocabulary-agnostic: reads `skos:broader` generically, no Homosaurus
  specifics.
- [x] The Hugo module can build a subject breadcrumb from `broader` without touching
  the graph (data now present -- see the Hugo-side note in Delivered).

## Delivered (commit pending)

`project/project.go`:

- **`buildBroaderIndex(ds)`** -- a second authority index alongside `buildLabelIndex`:
  scans every graph for `skos:broader` quads with an **IRI** subject and **IRI** object,
  mapping term URI -> sorted, deduped parent URIs. Nil when the corpus has no
  `skos:broader`. Provider/vocabulary-agnostic (no Homosaurus specifics); non-IRI broader
  objects (blank nodes, literals) are ignored.
- **`Subject.Broader []string`** (`json:"broader,omitempty"`), populated in
  `subjectsAndTags` from the index. Id-only, per the task contract: a parent's label
  resolves from the parent's own `Subject`/authority record (or the `facets.json`
  subject list), joined by id.
- **`SubjectFacet.Broader []string`** too, so a facet sidebar can drive
  broader/narrower drill-down; `facets.json`'s subject list doubles as the global
  id -> {labels, broader} dictionary a consumer joins parents against.
- **SchemaVersion 4 -> 5** with the cascade: Hugo `catalogSchemaVersion` 4->5,
  `hugo/README.md`, and the exampleSite `catalog.json`/`facets.json` (bumped to v5 and
  extended with a real resolvable hierarchy: "Transgender people" -> broader "Gender
  identity", the parent added as its own subject so the static demo resolves the label).

**Verified:** `TestProject`/`TestFacets` assert `Broader` on the Subject + facet;
`TestSubjectBroader` covers multi-parent sort+dedup, IRI-only filtering, and the
no-broader (nil) case. CLI `lcat project` over an authority-graph dataset emits
`broader` in both `catalog.json` and `facets.json`. The real OverDrive corpus
reprojects to v5 with **0** `broader` (its subjects are uncontrolled tag strings, not
IRIs -- correct). exampleSite builds clean on v5.

**Hugo-side note:** `broader` now reaches the module -- per Work via the content
adapter's `subjectList` param, and globally via `facets.json` (with parent labels).
Rendering an actual breadcrumb partial (resolving parent URIs -> labels via the
facets.json dictionary) is a small, self-contained follow-on left to the deploying
site/consumer; the primary consumer (qllpoc) renders its own trails from `catalog.json`.

**Optional, deferred (task item 3, "if cheap"):** `skos:altLabel` ->
`Subject.AltLabels map[string][]string` for search synonyms -- not needed to unblock
the hierarchy consumer, so held as a follow-on rather than widening this change.

## Refs

- `project/project.go` (`subjectsAndTags` ~L399, `Subject` struct, `buildLabelIndex`
  ~L440); `tasks/012` (labels), ARCHITECTURE §5 (controlled vocabularies as linked
  data).
- qllpoc `cmd/graphauthorities` (emits `authority:homosaurus` with broader),
  `tasks/045` (the bridge that needs it), `homosaurus-trails.json` (the existing
  qllpoc trails feature this would drive from the graph).
