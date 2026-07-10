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
