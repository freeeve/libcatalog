# 215 -- cover images: upload/remove per work, lcat:coverImage editorial quad, projector + OPAC + editor surfaces (058 item 2; zip batch deferred)

Opened 2026-07-09.

## Outcome

Shipped (feat tasks/215 commit), released v0.65.0. Key design find
that shrank the task: the OPAC cover slot already exists
(hugo/_partials/lcat-cover.html, gated by [params] covers=true,
reading extra.cover from tasks/022/025) -- so uploads simply feed that
same key EDITORIALLY: blob bytes at data/covers/<shard>/<workID>.<ext>
plus an editorial lcat:extra/cover statement, and buildExtraIndex now
overlays editorial extras over feed extras (the workindex summaries
already read all graphs).

- PUT/DELETE /v1/works/{id}/cover (librarian; jpeg/png/webp, 2MB cap;
  grain-describes guard; COVER_SET/COVER_REMOVE audited), public
  GET /covers/{file} with cache headers.
- lcat export -covers-out / [export] covers-out copies uploads flat
  into the site's covers/ dir the URLs point at.
- WorkEditor Cover panel (thumbnail, upload/replace/remove) beside
  Visibility; raw-body callRaw added to the API client.
- Verified live end-to-end on the playground: upload 200 -> editorial
  quad in the grain -> public GET image/png -> Project() Extra["cover"]
  = covers/<id>.png -> DELETE 204 removes statement and bytes (grain
  back to 0 cover statements). Unit lifecycle test covers types,
  phantom work, anon, and both directions.
- Deferred, still on 058: batch zip upload keyed by workId/ISBN, and
  attachments (lcat:attachment).
