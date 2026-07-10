# 332 -- DeleteUser silently orphans a departing user's shared macros/templates (now admin-reachable but not surfaced); ownership keyed on a recyclable email means re-issuing an address inherits its shared records -- follow-up to 292

Opened 2026-07-10. Split from tasks/292, which fixed the headline (an admin is
now the custodian of shared macros/templates and can delete an orphaned one) but
deliberately left two of its five expected bullets for here, because both are
larger than the CRUD-guard change 292 shipped.

## What 292 left open

**1. `DeleteUser` still silently orphans.** `DELETE /v1/users/{email}`
(`auth_handlers.go`) removes the account and nothing else -- it has no knowledge
of the `ITMPL#`/`MACRO#` partitions. After 292 the orphaned shared records are
no longer *unreachable* (an admin can delete them), but nothing tells the admin
they exist. An admin deleting a departing cataloger has no signal that the user
owned three shared macros now in everyone's list with a dead owner.

Options (292's bullet 2): `DeleteUser` should either refuse to orphan (409
listing the shared records that must first be reassigned or deleted), or reassign
them to the deleting admin, or offer a `POST /v1/macros/{id}/reassign`. Silently
leaving them is now recoverable but still invisible.

**2. Ownership is keyed on a recyclable string.** `OwnedMeta.Owner` is a bare
email address, compared directly in `writable()`. Re-creating an account with the
same address inherits every shared record the departed user owned -- a quiet
authorization surprise for a library that recycles role addresses like
`cataloging@` or `circulation@`. 292 documented this on `OwnedMeta` but did not
change it. A stable, non-recyclable user id as the ownership key is the real fix;
if the address must stay the key, that is a decision to make explicitly.

## Not urgent

292 removed the permanent-unreachable state, which was the sharp edge. Both items
here are correctness/UX hardening, not a live break. Do them together -- they both
touch how ownership and account deletion interact.
