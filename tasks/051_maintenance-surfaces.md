# 051 -- Maintenance surfaces (concerns, merge UI, duplicates, covers, items)

## Context

The remaining Koha Tier-1 cataloging surfaces, each a UI over machinery that
mostly exists: merge/split (CLI markers), duplicate detection
(`Resolver.Conflicts()`), catalog concerns (queue item type), tombstone/
suppress (delete stance), covers/attachments, relationships, and the minimal
`bf:Item` holdings model (call number/location/barcode/note -- never
circulation state).

## Scope

1. Concerns: `CONCERN` queue item type (freetext + workId + reporter),
   report-a-problem endpoint (anonymous, anti-abuse shared with suggestions),
   review screen actions resolve/dismiss/convert-to-edit.
2. Merge/split UI: MergeChooser (side-by-side field chooser writing editorial
   overrides on the survivor), split with instance pinning; clone
   (copy doc, strip provider keys, mint fresh ids, open as draft).
3. Tombstone (`lcat:tombstoned` + redirect entry) and `lcat:suppressed` UI;
   projector honors both.
4. Duplicate-detection dashboard: Resolver conflicts + same-WorkKey pairs as a
   worklist with open-merge-tool.
5. Covers: upload to blob store + `lcat:coverImage` editorial quad; batch zip
   keyed by workId/ISBN. Attachments (`lcat:attachment`) same shape.
6. RelationshipsPanel (`bf:hasPart`/`partOf`/series + enumeration) and
   ItemsPanel (minimal bf:Item, batch-editable via the op machinery,
   CSV-exportable).

## Acceptance

- Tombstoned work disappears from projection with a redirect; suppress hides
  without redirect.
- Merge via UI reproduces CLI-merge semantics; duplicates worklist opens into it.
- bf:Item fields round-trip grain -> editor -> projection.
