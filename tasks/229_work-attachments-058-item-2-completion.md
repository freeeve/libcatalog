# 229 -- work-attachments (058 item 2 completion)

Opened 2026-07-09. The last piece of tasks/058 scope item 2 (covers
shipped in 215, zip batch in 220).

Generalize the covers machinery to arbitrary work attachments under
`lcat:attachment` editorial statements:

- Blob path `data/attachments/<shard>/<workId>/<filename>` (sanitized
  basename, [A-Za-z0-9._-], <=100 chars); 20MB cap; any content type.
- POST /v1/works/{id}/attachments?name=<filename> (librarian, raw
  body, grain-first with the describes-guard), DELETE .../{name}, GET
  list. Download serves application/octet-stream with
  Content-Disposition: attachment -- no inline render, so an uploaded
  HTML file is not an XSS surface.
- Staff-only: attachments are cataloging working material (scans,
  correspondence); NOT projected to the OPAC. Public surfacing, if
  ever wanted, is a later opt-in with its own review.
- Editor: AttachmentsPanel beside CoverPanel (list, upload, remove).
- Audit ATTACHMENT_ADD / ATTACHMENT_REMOVE. Clones do not carry
  attachments (the lcat:* drop in CloneGrain already covers the
  statements; bytes stay with the source).
