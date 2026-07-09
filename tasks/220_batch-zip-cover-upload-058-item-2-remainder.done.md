# 220 -- batch-zip-cover-upload (058 item 2 remainder)

Opened 2026-07-09. Split from tasks/058 scope item 2 (covers shipped in
tasks/215; this is the batch half).

## Outcome

Shipped in v0.70.0 (commit fb31c6f). `POST /v1/covers/batch` (librarian)
takes a zip named by work id or ISBN; ISBNs resolve through the work
index with separator-insensitive normalization, ambiguous ISBNs skip
rather than guess, and each applied cover rides the 215 grain-first
SetCover path with a per-entry result row and COVER_SET audit. The
Batch operations screen grows the uploader with the per-file report.

Verified live on the playground: a zip keyed one entry by work id, one
by hyphenless ISBN, one garbage name -- 2 applied (grain statement +
served bytes confirmed), 1 skipped with reason. Unit lifecycle plus
normalizeISBN table test.

058 item 2's remaining piece is generic attachments (lcat:attachment).
