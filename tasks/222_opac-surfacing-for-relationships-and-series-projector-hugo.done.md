# 222 -- opac-surfacing for relationships and series (projector + hugo)

Opened 2026-07-09. Follow-up to tasks/221, which added the editor and
grain layers only.

Surface the new statements publicly:

- Projector: emit each work's hasPart/partOf links (id + resolved
  title, dropping links to suppressed/tombstoned targets) and the
  instances' seriesStatement/seriesEnumeration into catalog.json.
  Needs a SchemaVersion decision -- additive fields may not warrant a
  bump; check how the reader handles unknown keys.
- Hugo: render "Part of"/"Contains" cross-links on the work page and
  the series statement (with enumeration) in the instance block.
- MARC exports: 490 from seriesStatement/Enumeration; 773/774 (or
  760/762) from the work links -- decide the mapping with the fidelity
  table, and record whatever stays out as KnownLoss.

## Outcome

Shipped in v0.72.0 (commit c06ebbd).

- **Schema v11**: Work.Relations ({id, title} hasPart/partOf,
  restricted to works in the projection -- suppressed/tombstoned
  targets drop in a post-pass, so no dead cross-links) and
  Instance.Series/SeriesEnumeration. catalogSchemaVersion bumped in
  lockstep per the 179 checklist; `lcat rebuild` full-rebuilds on the
  version change by design.
- **Hugo**: Related-works section (Part of / Contains, relLangURL
  cross-links) + series line in the instance block; i18n keys added;
  exampleSite fixture upgraded to v11 with a relation pair and a
  series. Verified at the rendered surface: en + es pages render the
  links language-prefixed; link_check (120 pages) and the axe a11y
  audit both clean.
- **MARC**: 490$a already round-trips via libcodex decode. $v filed as
  libcodex tasks/102 (both directions, no urgency). 773/774 recorded
  as KnownLoss -- host-item shaping needs per-target $w/title the bare
  {#idWork} references don't carry; honest omission until designed.

Adopters bump lcat and the hugo module together (the adapter fails
loudly on mismatch, as designed).
