# 334 -- copycat commit/stage/revert audit entries carry no workId, so an imported record's History tab never shows where it came from -- follow-up to 299

Opened 2026-07-10. Split from tasks/299, which added the audit-log *reader* (the
no-workId system entries now have a screen) but left this backend audit-*emission*
change, because it is a separate concern from the missing view.

## The gap

`COPYCAT_COMMIT` (`suggest/copycat.go:~439`) names only the batch:

```go
s.audit(ctx, suggest.AuditEntry{
	Action: "COPYCAT_COMMIT", Actor: actor,
	Note: fmt.Sprintf("batch %s: %d committed, %d skipped (%s), %d grains touched", …),
})
```

No `WorkID`. `COPYCAT_STAGE` and `COPYCAT_REVERT` are the same. So an imported
record's **History tab is silent about its own import** -- its history begins at
the first manual edit and never says where it came from. A record *cloned* in the
editor does show its provenance, because `WORK_CLONE` (`clone_handlers.go:~54`)
carries `WorkID: newID`. Copy cataloging -- where "where did this record come
from, and who committed it?" is the entire point of provenance -- shows nothing
in the one place a cataloger would look.

## Shape of the fix

`records_handlers.go` is the precedent, one layer over: for a batch edit it writes
**both** a per-work `BATCH_EDIT` (visible in each work's History) **and** a
run-summary `BATCH_EDIT` (the aggregate). Copycat commit should do the same: emit
a per-work `COPYCAT_COMMIT` (carrying `WorkID`, and a note naming the source
target/batch) alongside the existing batch-summary entry. Then the imported
record's History tab shows its import, and the audit-log screen (tasks/299) still
shows the summary.

The audit-log screen already renders both shapes, so no reader work is needed --
this is purely about giving the per-work entry a `WorkID` to be found by.

## Not urgent

299 made every no-workId entry readable on the audit screen, so the commit
summaries are no longer invisible; what remains is that they are not reachable
from the *record's own* History. That is a provenance-completeness gap, not a
data-loss one.

## Outcome -- shipped in libcat v0.147.0 (minor)

`copycat.Commit` now captures the ingest run's resolved work IDs (`res.WorkIDs`,
previously discarded via `_`) and emits one per-work `COPYCAT_COMMIT` audit entry
-- carrying that work's `WorkID`, the run ID, and a `imported from <source> (batch
<id>)` note -- alongside the existing batch-summary entry (now also tagged with
`RunID`). An imported record's History tab, which filters audit by `WorkID`, now
shows its import; the audit-log screen (tasks/299) still shows the aggregate.

**Scope: commit only.** Per-work `COPYCAT_STAGE`/`COPYCAT_REVERT` were considered
and deliberately not added -- staging is a pre-commit preview so new records have
no work ID yet, and revert removes the imported grains, so a per-work entry would
attach to a work that is being deleted. Commit is the moment a work comes into
existence from a copied record, which is the entire provenance question.

- `backend/copycat/copycat.go` -- `Commit` captures `committedWorks` and the
  per-work emission loop; summary entry carries `RunID: b.ID`.
- `ingest/ingest.go` -- `Result.WorkIDs` (added earlier this arc) surfaces the
  resolved work IDs the loop iterates.
- Test: `copycat_test.go` `TestCommitAuditsPerWorkProvenance` wires a `Queue`
  into the service, stages+commits, and asserts one summary entry plus one
  per-work entry per committed work (each with a `w`-prefixed WorkID and
  `RunID == batchID`).

**Verified live on :8481:** staged a 15-record MARC upload, committed it, then
`GET /v1/audit?month=&workId=<committed work>` returns exactly the
`COPYCAT_COMMIT` provenance row (`imported from upload (batch ...)`) -- zero rows
before this change. Backend copycat/suggest/httpapi suites green.

Closes the record-History half of the U7 provenance probe (libcat-e2e), deferred
in the 299 doneness note.
