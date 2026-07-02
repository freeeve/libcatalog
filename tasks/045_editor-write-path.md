# 045 -- Editor write path (ops, drafts, diff preview)

## Context

Makes the editor editable: the SPA emits op lists, the server turns them into
editorial quad deltas with override markers, ETag conflicts surface as a
three-way flow, drafts autosave, and every publish shows its exact quad delta
first. Completes the end-to-end editing milestone with 037/041/042.

## Scope

1. `backend/editor/apply.go`: ops -> doc mutation -> doc-to-quads diff ->
   editorial delta + `lcat:overrides` markers -> publisher; audit entry with
   the op list.
2. SPA: editable ProfileForm (add/remove repeatable values, enum/vocab/authority
   value sources), save/publish keyboard flow, autosaving drafts (op list,
   rebase prompt on conflict), DiffPreview panel (dry-run quad delta), History
   tab (audit entries for the Work).
3. 409 three-way flow: base/yours/theirs field-level display, retry.

## Acceptance

- Edit a feed value -> publish -> projection shows the editorial value; revert
  restores feed.
- Draft survives reload; publish replays against current etag.
- Untouched fields produce zero quads (no-op save is a no-op).
