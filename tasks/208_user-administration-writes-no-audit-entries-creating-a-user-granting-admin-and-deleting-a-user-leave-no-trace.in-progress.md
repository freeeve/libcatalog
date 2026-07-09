# 208 -- user administration writes no audit entries: creating a user, granting admin, and deleting a user leave no trace

Filed from libcat-e2e on 2026-07-09 (cross-repo ask).

## Symptom

Every other mutating surface in libcat writes an `AuditEntry`. The four user
administration routes write none. Creating an account, granting it `admin`, and
deleting it are invisible to `GET /v1/audit`.

Measured with `node harness/probe_audit.mjs` (2026-07-09). The profile writes
are the control -- they run first, against the same service, in the same month,
and prove the audit trail is working before the probe concludes anything is
missing from it:

| # | Action | Audit entries |
|---|--------|---------------|
| A6 | **control**: `PUT` profile ×2, `DELETE` profile | 4 → 7 (`PROFILE_EDIT`=2, `PROFILE_REVERT`=1, `actor=eve@example.org`) |
| A9 | `POST /v1/users` → 201 | 7 → 7 |
| A9 | `PUT /v1/users/{email}/roles {"roles":["admin"]}` → 204 | 7 → 7 |
| A9 | `DELETE /v1/users/{email}` → 204 | 7 → 7 |

Rows naming the affected user: **0**. Distinct actions recorded this month:
`BATCH_OPS, PROFILE_EDIT, PROFILE_REVERT` -- no `USER_*` action exists.

## Root cause

`backend/httpapi/auth_handlers.go` contains no `WriteAudit` call, and it could
not make one: the audit service is never passed to it.

- `backend/httpapi/httpapi.go:131` -- `registerLocalAuth(mux, deps.Local, deps.Verifier)`
- compare `:163` -- `registerProfiles(mux, deps.Profiles, deps.Suggest, deps.Verifier)`
- compare `:140` -- `registerReview(mux, deps.Suggest, deps.Verifier, deps.Publisher)`

So `registerLocalAuth` (`auth_handlers.go:14`) has no `*suggest.Service` in
scope, and its handlers -- `POST /v1/users` (`:68`), `PUT /v1/users/{email}/roles`
(`:89`), `DELETE /v1/users/{email}` (`:107`) -- return their status codes and
write nothing else. `auth.FromContext(r.Context())` is available to name the
actor, exactly as `profiles_handlers.go:71` does, but is never called.

Coverage everywhere else is genuinely thorough -- `grep -rn "riteAudit(" --include=*.go`
finds callers in `batch/batch.go:283`, `publish/publisher.go:163`,
`publish/promote.go:89`, `authoritiesvc/service.go:328`, `copycat/copycat.go:461`,
`httpapi/records_handlers.go` (×5), `httpapi/maintenance_handlers.go` (×3),
`httpapi/marc_handlers.go:153`, `httpapi/items_bulk.go:106`,
`httpapi/profiles_handlers.go` (×2), `suggest/promotion.go` (×3),
`suggest/review.go` (×3). User administration is the one hole.

## Why it matters

This is the surface where the *hole is least acceptable*. Role grants are the
privilege boundary for every other audited action: an actor who can grant
themselves `admin` can then edit profiles, run batch ops and publish -- all of
which are dutifully recorded, while the grant that authorized them is not. An
audit trail that records the consequences but not the escalation cannot answer
"who gave this person the ability to do that", which is the first question asked
after an incident.

It compounds tasks/207 (no last-admin guard). Together: an admin can silently
demote the last admin, or delete an account, and nothing anywhere records that it
happened or who did it. There is also no `Users.svelte` -- this surface is driven
by hand with curl, so there is no UI-side history either.

`AuditEntry` (`suggest/review.go:34-42`) already carries everything needed:
`Actor`, `Action`, `Note`, and `Terms`.

## Expected

`registerLocalAuth` takes the `*suggest.Service` (as `registerProfiles` does) and,
on each successful mutation, writes an entry naming the actor and the target:

- `POST /v1/users` → `USER_CREATE`, `Note: <email>`, roles in `Terms`
- `PUT /v1/users/{email}/roles` → `USER_ROLES`, `Note: <email>`, new roles in `Terms`
- `DELETE /v1/users/{email}` → `USER_DELETE`, `Note: <email>`

Role changes in particular should record the *old* roles as well as the new, so
a demotion is legible without diffing against a prior entry.

## Repro

```
cd ~/libcat-e2e && node harness/probe_audit.mjs   # A9 FAIL (A6 control PASSes)
cd ~/libcat-e2e && node harness/retest.mjs        # reports 208 STILL-BROKEN
```

The probe creates and removes a sentinel profile and two sentinel users, and
asserts `eve@example.org` still holds `["admin"]` before it reports.

## Not bugs (verified clean this cycle)

The audit read surface itself is sound: `GET /v1/audit` is librarian-gated
(anonymous → 401, moderator → 403, matching the role comment at `auth/auth.go:25-27`);
a missing or malformed `month` → 400; a month with no activity → 200 with an
empty array; the `workId` filter works; and entries come back newest-first, as
`review.go:350` promises (`PROFILE_REVERT` first, `PROFILE_EDIT` last, timestamps
strictly descending).

One cosmetic note, not filed: `monthPattern` is `^\d{4}-\d{2}$`, so `month=2026-99`
is accepted and returns 200 with an empty list rather than 400. Harmless, since
the month is only a partition key.
