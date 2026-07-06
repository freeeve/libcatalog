# 142 -- facet display labels: language names, labeled classifications

Filed from queerbooks-demo (2026-07-06, Eve's report). Do not let a
queerbooks session edit this repo -- implement here. Companion to 141
(vocabulary-split subjects): both are "the facet shows raw identifiers".

## Languages

The languages facet renders ISO 639-2 codes ("eng 47566, spa 712, ger 334").
The graph is right (language IRIs); the presentation layer should map codes
to display names -- ideally localized per site language (the module already
localizes chrome via i18n; a langNames i18n table or a shipped 639-2 ->
name data file both work). Applies to the facet sidebar, the language term
pages, and the work detail's Language row. Same story in the backend
work-index panel.

## Classifications

codexbf.Classification carries {Class, Value, Source} -- no display label --
and the projection flattens to one string, so a deployment must choose
between the code (right for MARC 084) and the human text (right for the
facet). queerbooks chose text for its BISAC hydration (Value = "Fiction /
Romance / Contemporary", Source bisacsh) and documented the MARC trade-off;
proper support = a Label alongside Value (libcodex Classification field +
crosswalk: 084 $a code with the label riding a display-only channel, or
$2-source-aware rendering), projector emits {value, label}, facet shows the
label. Then queerbooks flips back to codes in Value.

## Done (schema v9)

Languages -- presentation-layer mapping, graph unchanged:

- Shipped the LOC 639-2/B code -> English name table as module data
  (hugo/data/lcat/languageNames.toml, generated from the same source as the
  backend picker, backend/ui/src/lib/languages.ts, tasks/080).
- New partial lcat-lang-name.html resolves i18n key "lang-<code>" (per-site
  localization/correction) -> shipped table -> raw code. Wired into the
  facet sidebar, language term-page titles, the /languages/ landing, and
  the work detail Language row (pagefind filter now carries the name).
  exampleSite/i18n/es.toml demonstrates overrides (Inglés/Español/Japonés).
- Backend: nothing to do -- the work editor already labels language IRIs
  via the tasks/080 picker table (ProfileForm iriTerm/languageTerm). No
  work-index/works-list surface in this repo renders a language code; if
  the queerbooks admin shows one, that's a downstream (or stale-build)
  issue.

Classifications -- label channel through the projection:

- project.Classification {value, label,omitempty}: Work.Classifications is
  now []Classification; the label is read from the classification node's
  rdfs:label (the display-only channel), the code stays
  bf:classificationPortion. Facets.Classifications is []ClassificationFacet
  {value, label, count}. SchemaVersion 8 -> 9; hugo module pin and
  exampleSite fixtures bumped (fixtures carry BISAC heading labels).
- Hugo: adapter passes code strings to the taxonomy plus classificationList
  (objects) for the detail row; facet sidebar/term pages/landing show the
  label falling back to the code (new cached partial
  lcat-classification-labels.html mirrors the tasks/141 subject-labels
  map). A label-less corpus renders exactly as before.
- backend CSV export: classifications column now prefers the label
  (matching the subjects column); languages column unchanged (codes).
- libcodex side filed as ../libcodex/tasks/090_classification-display-label.md
  (uncommitted there): add Classification.Label, emit/read rdfs:label on
  the node. Once shipped, ingest/overdrive can set Label from the feed's
  BISAC.Description, and queerbooks flips Value back to codes.

## Update: libcodex v0.14.0 adopted (same day)

libcodex shipped Classification.Label (their tasks/090) in v0.14.0; both
go.mods bumped and ingest/overdrive now sets Label from the feed's
BISAC.Description, so the OverDrive route lights the whole chain: feed
heading -> rdfs:label on the classification node -> projector {value,
label} (schema v9) -> facet shows the heading, MARC 084 keeps the code.
