# 034 -- Moderation queue + audit trail

## Context

Staff-facing half of the queue: review decisions (approve/reject/substitute/
tombstone, dispute context), manual terms, folk accept/block, and the audit
trail -- qllpoc's decision vocabulary generalized to any vocab and any queue
item type (SUGGESTION now; CONCERN and PROMOTION arrive in later tasks on the
same shape).

## Scope

1. Routes: `GET /v1/queue?status=&scheme=&provenance=` (moderator+; STATUS#
   index, cursor pagination, supporter-count sort), `POST /v1/review`
   (moderator+ approve/reject; publish flag gated to CanPublish),
   `POST /v1/terms` (librarian manual term, born-APPROVED; folk accept/block),
   `GET /v1/audit?month=YYYY-MM` (librarian).
2. Review semantics: conditional status flips (concurrent-moderator safe),
   optional substitute term, optional tombstone; decisions stamp
   reviewedBy/reviewedAt/reviewNote.
3. `AUDIT#<YYYY-MM>` items written by every mutating handler (actor, action,
   payload summary); monthly query.

## Acceptance

- Moderator approves but cannot publish (403); librarian publish path flags
  items approved-unpublished (publisher drains them in tasks/036).
- Concurrent review race resolves via version conditions.
- Audit entries appear for every mutation and query by month.
