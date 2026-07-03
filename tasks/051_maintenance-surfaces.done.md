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

- [x] Tombstoned work disappears from projection with a redirect; suppress hides
  without redirect. (Projector tests: tombstone-with-successor redirects like a
  merge, tombstone-without leaves an empty-target entry, suppress hides with no
  redirect, both restore cleanly.)
- [x] Merge via UI reproduces CLI-merge semantics; duplicates worklist opens into
  it. (The Duplicates screen merges through POST /v1/works/merge -- the same
  lcat:mergedInto marker path as the CLI -- with a side-by-side doc compare and
  survivor pick.)
- [x] bf:Item fields round-trip grain -> editor -> projection. (httpapi test:
  PUT items -> GET reads back -> projection carries them on the Instance;
  replace shrinks with no stale statements.)

## Delivered (2026-07-02)

- **Visibility** (`bibframe/visibility.go`): `lcat:tombstoned` (object = the
  successor Work IRI, or literal "true" for gone) and `lcat:suppressed`,
  editorial so they survive re-ingest. The projector skips both;
  `project.Redirects` folds tombstone successors into the redirect map and
  emits empty-target entries for no-successor tombstones. HTTP:
  GET/POST `/v1/works/{id}/visibility`; SPA VisibilityPanel in the editor
  header (suppress toggle, tombstone with optional redirect target, restore).
- **Items** (`bibframe/items.go`): minimal `bf:Item` on skolem nodes under
  the Instance -- bf:shelfMark/bf:physicalLocation + lcat:barcode/
  lcat:itemNote literals, never circulation state. Projection: `Instance.
  Items` in catalog.json. HTTP: GET/PUT `/v1/works/{id}/items` (wholesale
  per-instance replace). SPA ItemsPanel per instance in the editor.
- **Duplicates + merge UI**: GET `/v1/duplicates` groups Works by clustering
  key from the identity scan (titles joined from summaries); the Duplicates
  screen expands a group into a side-by-side field compare (both docs),
  survivor radio, and merges losers via the existing endpoint. Split UI:
  instance checkboxes + split action in the editor (existing endpoint).
- **Deferred to tasks/058**: catalog concerns (CONCERN queue type + anonymous
  report endpoint), covers/attachments, RelationshipsPanel, clone, item CSV
  columns, and a richer per-field merge chooser (adopting individual field
  values pre-merge). The acceptance surfaces above are complete.
