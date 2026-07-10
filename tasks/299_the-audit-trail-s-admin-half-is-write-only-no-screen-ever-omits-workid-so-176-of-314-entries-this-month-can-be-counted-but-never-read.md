# 299 -- the audit trail's admin half is write-only: no screen ever omits workId so 176 of 314 entries this month can be counted but never read

Filed from libcat-e2e on 2026-07-10 (cross-repo ask).

tasks/259 asked which admin mutations *land* an audit entry. This is the other half of the
question: **of the entries that do land, which can anyone ever read?**

The route is right. `GET /v1/audit?month=YYYY-MM[&workId=…]` (`review_handlers.go:181-199`)
makes `workId` optional; without it, the whole month comes back, newest first. The storage is
right too -- `AUDIT#<YYYY-MM>` / `<ts>#<rand>` (`review.go:430-435`), and `Audit()` even
re-sorts on the timestamp to repair a legacy key format that mis-ordered trailing zeros
(`:439-445`). Nothing here is wrong.

**The SPA never asks for a month.**

```
$ grep -rn 'fetchAudit' backend/ui/src --include=*.svelte
backend/ui/src/components/HistoryPanel.svelte:27:      const res = await fetchAudit(month, workId);
```

One call site, and it always passes a work. `screens.ts:38-54` mounts thirteen sidebar
screens; none of them is an audit log. So every entry written **without** a `WorkID` is
invisible in the product -- not filtered out, not paginated away: unreachable.

## Symptom

Measured against the running playground (`:8481`), current month:

```
310 audit entries
  ATTACHMENT_ADD       workId=yes   3
  ATTACHMENT_REMOVE    workId=yes   3
  BATCH_EDIT           workId=NO    3      <- the run summary
  BATCH_EDIT           workId=yes   6      <- the per-work rows
  BATCH_OPS            workId=NO    6      <- the run summary
  BATCH_OPS            workId=yes  18      <- the per-work rows
  COPYCAT_COMMIT       workId=NO   54
  COPYCAT_REVERT       workId=NO    3
  COPYCAT_STAGE        workId=NO   54
  COVER_REMOVE         workId=yes   9
  COVER_SET            workId=yes  18
  MARC_EDIT            workId=yes   3
  PROFILE_EDIT         workId=NO    7
  PROFILE_REVERT       workId=NO    7
  RECORD_EDIT          workId=yes   6
  USER_CREATE          workId=NO   16
  USER_DELETE          workId=NO   16
  USER_ROLES           workId=NO    6
  VISIBILITY_tombstone workId=yes  57
  WORK_CLONE           workId=yes   6
  WORK_RELATE          workId=yes   3
  WORK_UNRELATE        workId=yes   6
```

**176 of 314** (the four the probe adds) carry no `workId`. Every one of them is written,
stored, and returned by the API. None can be displayed.

The absolute counts move -- the playground is restarted by hand and its audit partition starts
over. A later run measured `60 of 106`. **The ratio is the finding: consistently a little over
half of a month's audit entries have no reader**, and the *set of actions* is invariant --
`BATCH_EDIT`, `BATCH_OPS`, `COPYCAT_COMMIT`, `COPYCAT_REVERT`, `COPYCAT_STAGE`, `PROFILE_EDIT`,
`PROFILE_REVERT`, `USER_CREATE`, `USER_DELETE`, `USER_ROLES`.

Driven through the real SPA: logged in as an admin, opened a work's **History** tab, then
visited the dashboard and all twelve sidebar screens, recording every request to `/v1/audit`.

```
GET /v1/audit calls issued: 1
  with workId:    1     (HistoryPanel, from the work editor)
  without workId: 0
nav offers: Works, Authorities, Vocabularies, Batch, Macros, Exports, Import,
            Duplicates, Withdrawals, Queue, Promotions, Profiles
```

They are not *lost*. `Stats()` (`stats.go:50`) rolls the same entries into the Dashboard's
"Editing activity", so `PROFILE_EDIT: 7` shows up as a **count**. Who did it, when, and to
which profile cannot be reached from anywhere.

### The record's own creation is missing from its history

`WORK_CLONE` names the work it produced (`clone_handlers.go:54`, `WorkID: newID`, note
`"cloned from …"`). `COPYCAT_COMMIT` names only the batch:

```go
s.audit(ctx, suggest.AuditEntry{                                     // copycat.go:439-443
	Action: "COPYCAT_COMMIT", Actor: actor,
	Note: fmt.Sprintf("batch %s: %d committed, %d skipped (%s), %d grains touched",
		b.ID, b.Committed, b.Skipped, b.Policy, len(changed)),
})
```

Measured: 6 `WORK_CLONE` entries, all carrying their work; 54 `COPYCAT_COMMIT` entries, none
carrying one. **A record cloned in the editor shows its provenance in its History tab. A
record imported by copy cataloging shows nothing** -- its history begins at the first manual
edit, and never says where it came from. `COPYCAT_STAGE` and `COPYCAT_REVERT` are the same.

### The split is deliberate, which is what makes it a gap

`records_handlers.go:433-441` writes **both** shapes for one batch run:

```go
queue.WriteAudit(…, suggest.AuditEntry{ WorkID: res.WorkID, Action: "BATCH_EDIT", … })  // per work
…
queue.WriteAudit(…, suggest.AuditEntry{ Action: "BATCH_EDIT", Actor: id.Email, RunID: runID,
	Note: suggest.RunNote{ Selection: …, Matched: …, Applied: …, Rewritten: …, Failed: … } })
```

Measured: 6 per-work `BATCH_EDIT` rows (visible in each work's History) and 3 run-summary rows
(invisible). The summary is the only place the run's `RunNote` lives, and `RunNote`
(`review.go:78-86`) is `Selection`, `Matched`, `Applied`, `Rewritten`, **`Failed`**, `Added`,
`Removed`, `Works []string` -- the whole shape of what a batch operation did. Somebody wrote a
`RunID` and that struct for a reader that does not exist.

## Root cause

`backend/ui/src/components/HistoryPanel.svelte:27` is the SPA's only reader of the audit
trail, and it is a per-work panel:

```svelte
// Audit trail for one work: the picked month's entries (current month by
// default, the month input goes back), action/actor/time/note per row.   :1-2
const res = await fetchAudit(month, workId);                              // :27
```

`api.ts:274` already supports the other call -- `fetchAudit(month)` with `workId` undefined
sets no parameter and gets the whole month. Nobody makes it. There is no `Audit.svelte`, no
`audit` route in `screens.ts`, and no nav entry.

The server-side filter is **correct** and should stay: a work's history must not list somebody
else's user administration. The missing piece is the unfiltered view.

## Why it matters

**An audit trail nobody can read is not an audit trail.** `USER_CREATE`, `USER_ROLES`,
`USER_DELETE` are exactly the events an audit log exists for -- who granted whom admin, and
when. They are recorded faithfully and shown nowhere. The only way a librarian sees them is
`curl`. The role model already treats reading them as a capability a librarian has and a
moderator does not (`auth.go:24-26`); the product never exercises it.

**It is the admin half that is invisible, and the admin half is the sensitive half.** Every
`workId`-bearing action (record edits, covers, attachments, visibility) is visible in that
work's History tab. Every action about *the system* -- users, roles, editing profiles,
imports, batch runs -- is not. The trail is complete for the low-stakes half and unreadable
for the high-stakes half.

**A batch run's failures are recorded and unreadable.** `RunNote.Failed` is the number of
works a batch operation could not rewrite. It exists only on the summary row.

**An imported record cannot be traced to its import.** For copy cataloging -- where the
question "where did this record come from, and who committed it?" is the entire point of
provenance -- the History tab is silent.

**This is a UI gap, not a data-model gap**, which is the good news: everything needed is
already stored and already served.

## Expected

- **Add an audit-log screen.** A `/audit` route with a month picker, reusing the row markup
  `HistoryPanel` already has, calling `fetchAudit(month)` with no `workId`. Librarian-gated,
  as the route already is -- `auth.go:24-26` says a moderator *"cannot publish to the graph,
  add manual terms, or **read the audit log**"*, so the role model already anticipates a
  reader. A filter by actor and by action would make it usable; the data has both.

  Realistically the Dashboard's "Editing activity" should link into it: it already shows
  `ByAction` counts computed from these very entries, and every count is currently a dead end.

- **Give `COPYCAT_COMMIT` the works it committed** (or write a per-work `COPYCAT_COMMIT`
  alongside the batch summary, the way `BATCH_EDIT` does). A record's History tab should say
  where the record came from. `WORK_CLONE` is the precedent, one file over.

- **Then decide what the run-summary rows are for.** If the audit screen renders them, `RunID`
  becomes a grouping key and `RunNote` finally has a reader. If it does not, they should not
  be written.

- **Keep the `workId` filter as it is.** It is correct, and a "fix" that leaked admin rows
  into a single work's history would be worse than the gap. The probe's `U2` asserts this so
  that a future change cannot quietly do it.

## Repro

```bash
cd ~/libcat-e2e && node harness/probe_audit_readable.mjs   # U4, U5, U7
cd ~/libcat-e2e && node harness/retest.mjs                 # check t299
```

Read/write against the playground on `:8481`. The probe creates and deletes a sentinel
librarian, and overrides the shipped `work-monograph` profile and reverts it in a `finally`
(as tasks/295 does) -- four admin actions chosen because none of them is about a work. It
stages nothing and saves no work.

Its controls carry the argument. `U1` shows the four actions **do** land entries and that all
four carry no `workId` -- so `U4` is about entries that exist, not about an empty log. **`U3`
is the one that matters: opening a work's History tab issues a real `GET /v1/audit` and
renders rows**, so the audit surface is wired and `U4` reports a missing *view*, not a missing
*feature*. `U2` asserts the `workId` filter is correct, pinning the behaviour a careless fix
might break. `U6` asserts the sentinel user is gone and the profile is back.

By hand:

```bash
TOK=...   # an admin token
curl -s -H "Authorization: Bearer $TOK" "localhost:8481/v1/audit?month=$(date -u +%Y-%m)" \
  | jq -r '.entries[] | "\(.action)\t\(if .workId then "workId" else "NO workId" end)"' \
  | sort | uniq -c | sort -rn
```

Then open the SPA and look for the screen that shows any of the `NO workId` rows.
