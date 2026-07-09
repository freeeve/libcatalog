# 259 -- admin configuration changes leave no audit trail: vocabulary source register/install/remove/delete and copycat target add/delete write no audit entry, while USER_CREATE and COVER_SET do

Filed from libcat-e2e on 2026-07-09 (cross-repo ask).

Found by inverting a question my own tracker had been answering the easy way. The `L7`
row read *"Coverage: `WriteAudit` called from batch, publish, promote, authorities,
copycat, records, maintenance, marc, items_bulk, profiles, promotions, review"* -- ✅.
That enumerates the packages that **do** call it, and never asks which admin-only
mutating routes **do not**.

Six of them do not.

## Symptom

Measured on :8481 by reading `GET /v1/audit?month=2026-07` before and after each call
and diffing the entries:

```
2012 entries in 2026-07 at the start

POST   /v1/users                          -> USER_CREATE  actor=eve@example.org
                                             note="zz-e2e-audit@example.org"  workId=null   <- control

POST   /v1/vocabsources                   -> 0 new audit entries
PUT    /v1/vocabsources/{name}/snapshot   -> 0 new audit entries
DELETE /v1/vocabsources/{name}/snapshot   -> 0 new audit entries
DELETE /v1/vocabsources/{name}            -> 0 new audit entries
POST   /v1/copycat/targets                -> 0 new audit entries
DELETE /v1/copycat/targets/{name}         -> 0 new audit entries

DELETE /v1/users/{email}                  -> USER_DELETE  note="zz-e2e-audit@example.org"   <- control
```

The two controls bracket the six failures: the log accepted a write immediately before
and immediately after, so "0 new entries" is not the log being closed, full, or
misread. `USER_CREATE` also carries **no `workId`**, which settles the obvious
objection -- the trail is not work-scoped.

## Root cause

Simply absent. `backend/httpapi/vocabsources_handlers.go` mounts five admin-only
mutating routes and contains no reference to `WriteAudit` or `AuditEntry` at all:

```
mux.Handle("POST   /v1/vocabsources",                  admin(...))   // :42
mux.Handle("DELETE /v1/vocabsources/{name}",           admin(...))   // :55
mux.Handle("POST   /v1/vocabsources/{name}/download",  admin(...))   // :63
mux.Handle("DELETE /v1/vocabsources/{name}/snapshot",  admin(...))   // :73
mux.Handle("PUT    /v1/vocabsources/{name}/snapshot",  admin(...))   // :84
```

`backend/httpapi/copycat_handlers.go:30` and `:42` (`POST` / `DELETE
/v1/copycat/targets`) likewise. (`backend/copycat/copycat.go` *does* audit -- but the
staging and commit of records, not the configuration of where records come from.)

The pattern they should follow is one file away, `backend/httpapi/auth_handlers.go:29`:

```go
queue.WriteAudit(r.Context(), suggest.AuditEntry{Action: action, Actor: actor, Note: note, Terms: terms})
```

No `WorkID`. Entries partition by month, not by work (`backend/suggest/review.go:416`,
`PK: "AUDIT#" + now.Format("2006-01")`), so a configuration entry needs nothing the
struct does not already have.

**The one partial exception, stated plainly.** The *download* path does persist a
record: `vocabsrc.Job` (`backend/vocabsrc/download.go:40-50`) carries `Requester`,
`Source`, `Scheme`, `Status`, `Terms` and `Error`. But it is written with
`ExpireAt: s.clock().Add(jobTTL)`, `jobTTL = 7 * 24 * time.Hour`
(`download.go:52,85`), so it evaporates after a week; it covers only downloads, not the
`PUT .../snapshot` upload, the removal, the source deletion, or targets; and it is a
job-status record, not an audit trail. It is evidence the information is *available* at
the call site, not evidence the trail exists.

## Why it matters

These are among the highest-blast-radius controls on the admin surface, and they are
the ones that keep no record of who pulled them.

- `DELETE /v1/vocabsources/{name}/snapshot` uninstalls a vocabulary. On this
  deployment `lcsh` carries **513,125 terms** (measured, `GET /v1/vocabsources`).
  Every subject heading that resolves through it stops resolving. Nothing records that
  it happened, when, or by whom.
- `PUT /v1/vocabsources/{name}/snapshot` installs terms straight into the live index
  from a hand-supplied dump; `POST .../download` fetches them from an operator-supplied
  URL. The provenance of every controlled term a cataloger later picks traces back to
  that act, and the act is unrecorded (beyond a job row that expires in 7 days).
- `POST /v1/copycat/targets` decides which external server the library copies MARC
  records **from**. `PutTarget` writes with `store.CondNone`, so repointing a seeded
  target such as `loc-sru` at a different host is a silent overwrite -- it changes the
  source of truth for every subsequent copy-catalogued record and leaves nothing
  behind. Deleting a target is audited nowhere either.

The asymmetry is what makes this a defect rather than a preference: libcat has already
decided this surface is worth auditing. Creating a user is recorded. Changing a user's
roles is recorded, with the transition spelled out (`librarian -> admin`). Setting a
**cover image** is recorded (`COVER_SET`, `cover_handlers.go:130`). Deleting half a
million subject headings is not.

It compounds two open findings. **255**: deleting a source with a snapshot installed
succeeds silently and leaves an orphan row -- and leaves no trace of who did it.
**252**: `RemoveSnapshot` leaves its sidecar artifacts on disk, so the only surviving
evidence of an uninstall is eight orphan files with no actor and no timestamp.

Nothing here is corrupted. What is missing is the ability to answer *"who removed LCSH,
and when"* -- the exact question an audit log exists to answer.

## Expected

Audit the configuration surface the way the user surface is already audited.

- **`vocabsources_handlers.go`**: `VOCAB_SOURCE_CREATE`, `VOCAB_SOURCE_DELETE`,
  `VOCAB_SNAPSHOT_INSTALL` (note: term count, and `upload` vs the snapshot URL),
  `VOCAB_SNAPSHOT_REMOVE`, `VOCAB_DOWNLOAD_START`. The handler already has the actor
  (`auth.FromContext`) and the term count (`InstallInfo.Terms`).
- **`copycat_handlers.go`**: `COPYCAT_TARGET_SET` / `COPYCAT_TARGET_DELETE`, with the
  URL and protocol in `Note`.
- Follow `USER_ROLES`' example and record the **transition**, not just the act:
  `"lcsh: 513125 terms -> removed"` is worth far more than `VOCAB_SNAPSHOT_REMOVE`.

If some of these are deliberately unaudited, `L7`'s coverage claim should say which and
why, because the current shape reads as "everything mutating is audited".

## Repro

```bash
cd ~/libcat-e2e && node harness/probe_audit_coverage.mjs   # A2-A7
cd ~/libcat-e2e && node harness/retest.mjs                 # check t259
```

By hand, against :8481 as an admin. The `USER_CREATE` bracket is the point -- without
it, an empty diff proves nothing:

```bash
TOK=…
M=$(date -u +%Y-%m)
count() { curl -s -H "Authorization: Bearer $TOK" "localhost:8481/v1/audit?month=$M" | jq '.entries|length'; }

echo "before:            $(count)"
curl -s -XPOST -H "Authorization: Bearer $TOK" -H 'Content-Type: application/json' \
  -d '{"email":"zzaudit@example.org","password":"zz-e2e-pass-123","roles":["librarian"]}' \
  localhost:8481/v1/users >/dev/null
echo "after USER_CREATE: $(count)"      # +1

curl -s -XPOST -H "Authorization: Bearer $TOK" -H 'Content-Type: application/json' \
  -d '{"name":"zzaudit","scheme":"zzaudit"}' localhost:8481/v1/vocabsources >/dev/null
printf '<http://example.org/z/1> <http://www.w3.org/2004/02/skos/core#prefLabel> "Z"@en .\n' \
  | curl -s -XPUT -H "Authorization: Bearer $TOK" --data-binary @- \
      localhost:8481/v1/vocabsources/zzaudit/snapshot >/dev/null
curl -s -XDELETE -H "Authorization: Bearer $TOK" localhost:8481/v1/vocabsources/zzaudit/snapshot >/dev/null
curl -s -XDELETE -H "Authorization: Bearer $TOK" localhost:8481/v1/vocabsources/zzaudit >/dev/null
echo "after 4 vocab ops: $(count)"      # unchanged

curl -s -XDELETE -H "Authorization: Bearer $TOK" localhost:8481/v1/users/zzaudit@example.org >/dev/null
echo "after USER_DELETE: $(count)"      # +1 -- the log was open the whole time
```

(Remove the snapshot *before* the source, or you hit 255 and leave an orphan row. The
sidecar artifacts survive either way -- that is 252 -- so sweep
`~/libcat-playground/site/data/authorities/sidecar/zzaudit.*` afterwards.)
