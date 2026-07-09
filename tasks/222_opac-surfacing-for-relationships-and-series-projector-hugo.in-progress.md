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
