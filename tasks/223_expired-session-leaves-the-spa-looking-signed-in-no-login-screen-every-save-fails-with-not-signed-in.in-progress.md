# 223 -- expired session leaves the SPA looking signed in: no login screen, every save fails with 'not signed in'

Filed from libcat on 2026-07-09 (cross-repo ask).

## Symptom

When a cataloger's session dies while the editor is open, the shell never
notices. The header keeps showing their email, the nav keeps working, and every
save fails with an opaque `not signed in`. Nothing offers a way back in.

Measured on the 8481 playground (`ui/probe_session_expiry.mjs`), with a staged
tag in the work editor:

```
PASS S1  cataloger stages an edit                     chip for "zz-sess-995p" on screen=true
PASS S3  the save is retried once, then gives up      ops POSTs seen=1
PASS S4  the editor says something about auth         error shown: "not signed in"
FAIL S5  shell turns into the login screen            login form present=false; header still shows "eve@example.org"
FAIL S6  a signed-out user is not shown as signed in  header identity after the session died: "eve@example.org"
PASS S7  staged edit is still on screen (not lost)    chip survives the failed save=true
PASS S8  reloading recovers the session               after reload: login form=true
FAIL S9  staged edit survives the reload              present after reload=false
```

The probe injects the two conditions a real dead session presents, and both were
confirmed against the live server rather than assumed:

- an expired access token: `POST /v1/works/{id}/ops` with a dead bearer -> `401`
- a revoked refresh token: after `POST /v1/auth/logout`, `POST /v1/auth/refresh`
  on that token -> `401`, so `getToken()` returns `""`

The everyday route into this state needs no simulation at all: access tokens
live 900s (`expiresIn=900`), and signing out in a second tab runs `clearSession()`
which drops the shared `lcat-refresh` key from `localStorage` *and* revokes it
server-side. The first tab then keeps working, looking healthy, until its access
token ages out -- at most fifteen minutes later.

## Root cause

The auth gate keys off `sessionStore`, which is written in exactly two places:
at boot (`backend/ui/src/App.svelte:117`) and in `signOut()`
(`backend/ui/src/App.svelte:146`).

```svelte
// App.svelte:139-141
if (!$sessionStore && route.name !== "login") navigate("/login");
```

Nothing nulls the store when the API concludes the session is gone.
`backend/ui/src/lib/api.ts` detects it three times over --
`throw new ApiError(401, "not signed in")` at `:104` and `:126`, and
`throw new ApiError(401, "authentication failed")` at `:154` and `:678` -- and in
each case the error is handed to the calling screen, which renders it as text
and stops. `lib/auth.ts:236` `clearSession()` has already cleared the in-memory
token by then, so `session()` would return `null` on the next call; nobody calls it.

The file's own header comment states the intended contract, which is not
implemented:

```
// backend/ui/src/lib/api.ts:1-3
// Typed client for the cataloging API. Injects the bearer token and retries
// once through a refresh on 401; a second 401 surfaces as an ApiError the
// shell turns into the login screen.
```

Four screens (`Queue.svelte:89`, `Authorities.svelte:69`, `Promotions.svelte:68`,
`WorkSearch.svelte:223`) render the string `session expired -- sign in again`,
which is advice the shell gives no way to follow. `WorkEditor.svelte` does not
special-case 401 at all, so the cataloger sees only `not signed in` next to their
own name in the header.

## Why it matters

This is the failure mode a cataloger meets on an ordinary day: leave a record
open over lunch, sign out on the other tab, come back and keep typing. The UI
gives no signal that the work is no longer being saved. The one recovery a user
would try on their own -- reload the page -- lands on the login screen and
discards every staged edit (S9), because staged ops live in memory and the
draft autosave that would have preserved them is 401ing too.

Silent, unrecoverable loss of a cataloger's in-progress work is the worst thing
a cataloging client can do.

## Expected

- When an API call fails because the session is gone (`getToken()` returns `""`,
  or a retried request 401s again), the shell drops `sessionStore` and shows the
  login screen -- the contract `api.ts:1-3` already claims.
- The header must never show an identity for a session that no longer exists.
- Re-authenticating should return the cataloger to the record with their staged
  edits intact. Signing in from a session-expiry prompt is a resumption, not a
  new sign-in, so it should not run `resetScreenStates()`.

## Repro

```
cd ~/libcat-e2e && node ui/probe_session_expiry.mjs
```

Expect `S5`, `S6`, `S9` to flip to PASS. The probe mints its own sentinel record
via copycat, never lets a write land (the save is intercepted), and tombstones
the record on the way out. `harness/retest.mjs` carries the same check as `t223`.

## Outcome

Shipped in v0.73.0 (commit 7ffdc1a). S5 and S6 flip to PASS; S9 is
deliberately deferred to tasks/225 (see below).

Design: rather than routing to the login screen -- which unmounts the
editor and destroys the staged ops the task exists to protect -- the
shell re-auths IN PLACE. auth.ts notifies `onSessionExpired` on a
terminal refresh failure or a sibling tab's sign-out (keyed on an
explicit liveness flag: the 401-retry path clears the token fields
before the refresh runs, which defeated the first heuristic); api.ts
fires it when a refreshed request still 401s. The shell then clears
the header identity immediately (nothing with `.who` semantics remains)
and overlays a sign-in dialog on the still-mounted screen. Signing
back in is a resumption: identity restored, overlay gone, no
navigation, no resetScreenStates. Explicit sign-out keeps its full
reset; the dialog offers SSO where configured (page-reload cost noted)
and a discard-and-go-to-sign-in escape.

Verified with your probe against the rebuilt 8481 (S0-S8 PASS, S9
expected-fail) plus a resume-path probe your script doesn't cover:
overlay -> sign back in -> staged chip intact -> the failed save lands
(R1-R6 all pass, sentinel tombstoned).

S9 (staged edit survives a *reload*) needs client-side persistence of
staged ops with an etag guard -- a draft-machinery decision, split to
tasks/225 rather than bolted on here. The overlay removes the need to
reload on expiry; 225 covers the reloads that still happen.

## Verification (filer)

Fixed. Verified 2026-07-09 by `harness/retest.mjs` (`t223`), twice in a row:

```
FIXED  223  dead session -> login screen
       after a 401 save: login form shown, header identity cleared
```

The check stages a tag on a copycat-minted sentinel work, clears `lcat-refresh`
(what `logout()` does in another tab), answers the ops POST with 401 (what an
expired 900s access token gets), and clicks Save. The shell now drops the session
and renders the login form; `.who` is empty, where it previously still read
`eve@example.org`. `harness/retest.mjs` keeps the check so a regression reopens it.
