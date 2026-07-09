# 058 -- Maintenance remainder (concerns, covers, relationships, clone)

## Context

The tasks/051 scope items outside its acceptance criteria, split out when the
acceptance surfaces (visibility, duplicates+merge UI, bf:Item) shipped. Each
is a UI/plumbing layer over machinery that exists.

## Scope

1. **Concerns** (DONE -> tasks/210, v0.61.0; convert-to-edit deferred): `CONCERN` queue item type (freetext + workId + reporter),
   anonymous report-a-problem endpoint sharing the suggestion anti-abuse
   challenge, review-screen actions resolve / dismiss / convert-to-edit.
2. **Covers/attachments** (covers DONE -> tasks/215, v0.65.0; zip batch DONE -> tasks/220, v0.70.0; attachments remain): upload to the blob store + `lcat:coverImage`
   editorial quad (attachments same shape under `lcat:attachment`); batch
   zip upload keyed by workId/ISBN; projector surfaces the cover URL.
3. **RelationshipsPanel**: `bf:hasPart`/`bf:partOf`/series + enumeration in
   the editor (the write shapes exist via the editorial patch machinery).
4. **Clone** (DONE -> tasks/217, v0.67.0): copy doc, strip provider keys, mint fresh work/instance ids,
   open as a draft -- needs a create-work path (grain built from an
   editorial-only doc), which none of the current surfaces have.
5. **Item polish**: items column(s) in the CSV export; batch item edits
   through the tasks/047 op machinery.
6. **Merge chooser polish**: per-field adopt-left/adopt-right staging ops on
   the survivor before the merge (the tasks/051 compare view is read-only).

## Acceptance

- An anonymous concern lands in the queue, is reviewable, and resolves or
  converts to an edit with audit.
- A cover uploads, projects into catalog.json, and renders in the Hugo module.
- Clone produces an editable new work whose ids are fresh and whose provider
  keys are gone.
