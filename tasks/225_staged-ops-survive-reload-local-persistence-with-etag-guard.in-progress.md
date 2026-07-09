# 225 -- staged-ops survive reload (local persistence with etag guard)

Opened 2026-07-09. Split from tasks/223 (its probe's S9): staged editor
ops live in memory, so a reload -- the recovery a user tries on their
own -- discards them. The 223 re-auth overlay removes the *need* to
reload on session expiry, but reloads still happen (crash, accidental
Cmd-R, browser restart).

Sketch: mirror the editor session's staged op list to localStorage
keyed by workId as it changes; on editor mount, if persisted ops exist
for this work, offer them like the server-side pendingDraft banner
does (resume / discard). Guard on the doc etag: persisted ops staged
against a grain that has since changed must not silently re-apply --
offer with a warning or drop, decide with the draft machinery's
semantics. Clear on successful save, discard, and explicit sign-out
(privacy: shared terminals). Mind size limits (op lists are small) and
multi-tab writes (last-writer-wins is fine for a per-work key).

e2e's probe_session_expiry.mjs S9 is the acceptance check.

## Outcome

Shipped in v0.75.0 (commit 96baf4c). Every edit mirrors the staged op
list + base etag to localStorage synchronously (the 3s server autosave
stays the durable cross-device copy; the mirror works when auth does
not). On mount the mirror rides the existing resume-or-discard draft
banner -- preferred over the server draft in the same browser -- and
the banner's base-etag check covers the stale-grain guard. Cleared on
save, discard, draft discard, and explicit sign-out; sandbox skips it.
Bonus: signing in returns to the stashed pre-login hash, so a reload
mid-record comes back to the record, not the dashboard.

Verified end-to-end on the playground (Playwright): dead session ->
reload -> login -> lands back on the record -> draft offered -> resume
restores the staged tag -> save lands -> sign-out sweeps the mirrors.
6/6.

On the acceptance check: S9 as literally written (staged text visible
in the body immediately after reload) cannot pass -- the reload lands
on the login screen by design (S8 asserts exactly that). The staged
work now survives and is offered back after sign-in; suggested to e2e
that S9 extend through the sign-in step.
