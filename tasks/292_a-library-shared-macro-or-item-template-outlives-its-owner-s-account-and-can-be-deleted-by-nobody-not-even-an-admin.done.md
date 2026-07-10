# 292 -- a library-shared macro or item template outlives its owner's account and can be deleted by nobody, not even an admin

Filed from libcat-e2e on 2026-07-10 (cross-repo ask).

`backend/batch/owned.go` is the generic CRUD engine behind both owned-or-shared record
families (tasks/116): **macros** (tasks/065) and **item templates** (tasks/069). Its own doc
comment states the rule:

> *"one record per item, living in the owner's partition or the library-shared one,
> **owner-gated for writes**."* (`owned.go:13-15`)

It enforces exactly that, and nothing else. `updateOwned:75-77` and `deleteOwned:102-104` are
both

```go
if cm.Owner != owner {
    return zero, ErrForbidden
}
```

There is no role parameter anywhere in the file, and the handlers pass only `id.Email`. So a
**library-shared** record -- one any librarian can publish with a single click, and which then
appears in every librarian's list -- is writable by exactly one principal: the string in its
`Owner` field.

`DELETE /v1/users/{email}` (`auth_handlers.go:144-161`) deletes the account and nothing else.
It does not purge, reassign, or even notice the shared records that account owns.

**So a shared macro or item template survives its owner's account, remains visible to every
librarian, and can be removed by no one.** Not the admin who deleted the user. Not any other
librarian. There is no route, no screen, and no role that can do it.

## Symptom

Measured against the running playground (`:8481`) with two real accounts: `eve@example.org`
(**admin**) and a sentinel librarian.

**Item templates:**

```
B (plain librarian) POST /v1/item-templates  {label, shared:true}   -> 201  owner=zz-e2e-tmpl-user@example.org
A (admin)           GET  /v1/item-templates                         -> the shared template is listed
A (admin)           DELETE /v1/item-templates/{id}                  -> 403 {"error":"not the owner"}
A (admin)           PUT    /v1/item-templates/{id}                  -> 403
A (admin)           DELETE /v1/users/zz-e2e-tmpl-user@example.org   -> 204   (owner's account is gone)
A (admin)           GET  /v1/item-templates                         -> still listed, owner=zz-e2e-tmpl-user@example.org
A (admin)           DELETE /v1/item-templates/{id}                  -> 403
```

**Macros, same engine, same result:**

```
B POST /v1/macros {label, shared:true, ops:[…]}  -> 201  owner=zz-e2e-macro-user@example.org
A (admin) DELETE /v1/macros/{id}                 -> 403
A (admin) DELETE /v1/users/zz-e2e-macro-user@…   -> 204
A (admin) GET  /v1/macros                        -> still listed, owner=<deleted user>
A (admin) DELETE /v1/macros/{id}                 -> 403
```

The row is now permanent. It has an owner who cannot log in, and a library that cannot reach
it.

### The only way out is to resurrect the owner

Ownership is compared as a bare email string. Recreating an account with the same address
inherits every shared record the departed user owned:

```
A POST /v1/users {email:"zz-e2e-macro-user@example.org", roles:["librarian"]}  -> 201
B' (the new account) DELETE /v1/macros/{id}                                    -> 204
```

That is how this probe cleans up after itself, and it is the only recovery path that exists.
It is also a quiet authorization surprise in its own right: **re-issuing an email address hands
the new holder write access to the old holder's shared records.** For a library that recycles
`circulation@` or `cataloging@` role addresses, that is not hypothetical.

## Root cause

`backend/batch/owned.go`:

```go
// updateOwned replaces an item's definition. Only the owner may update;
// flipping Shared moves the record between partitions.
func updateOwned[T any](ctx context.Context, db store.Store, k ownedKind[T], id string, item T, owner string) (T, error) {
	...
	cm := *k.meta(&current)
	if cm.Owner != owner {                     // :75-77
		return zero, ErrForbidden
	}

// deleteOwned removes an owned item (shared or personal).
func deleteOwned[T any](ctx context.Context, db store.Store, k ownedKind[T], owner, id string) error {
	...
	m := *k.meta(&item)
	if m.Owner != owner {                      // :102-104
		return ErrForbidden
	}
```

`owner` is always `id.Email` from the request context. The caller's **roles never reach this
file**, so `adminOnly` cannot be expressed here even if a handler wanted it. Both handler sets
mount under `librarian(...)`:

```
batch_handlers.go:119  DELETE /v1/macros/{id}          -> svc.DeleteMacro(ctx, id.Email, r.PathValue("id"))
batch_handlers.go:166  DELETE /v1/item-templates/{id}  -> svc.DeleteItemTemplate(ctx, id.Email, r.PathValue("id"))
```

And `DeleteUser` (`auth_handlers.go:149`) has no knowledge of the `ITMPL#`/`MACRO#` partitions
at all. It guards `selfTarget` and `ErrLastAdmin`, then removes the account.

The asymmetry is the tell. **A record any librarian may publish to the whole library is
governed by a rule designed for a private record.** `getOwned` and `listOwned` already
distinguish the two cases -- they look in `owner` *and* `sharedPartition`. Only the write path
collapses them.

## Why it matters

**A one-click action creates permanent, library-wide state.** In `ItemsPanel.svelte:211` the
control is a button labelled `…shared`, sitting next to `Save row as template`. A cataloger
who clicks the wrong one has published a row to every colleague's dropdown, and -- unless they
personally delete it, which the SPA gives them no way to do (**tasks/293**) -- it is there for
good.

**Staff turnover is the normal case in a library.** Cataloging assistants graduate, volunteers
rotate, contracts end. Every account that ever published a shared macro leaves behind rows
nobody can touch. The list only grows, and the admin's only tool is `DELETE /v1/users`, which
makes it worse: before the account is deleted, the owner could at least have removed them by
hand with `curl`; afterwards, nobody can.

**Macros execute operations.** A shared macro is a saved list of `ops` a librarian applies to
records. An abandoned one -- pointing at a retired tag vocabulary, say -- stays in everyone's
list, one click from being applied, forever.

**The UI hides it perfectly.** `Macros.svelte:189` guards Edit and Delete with `{#if m.owner
=== me && !readOnly}`, so a shared macro owned by someone else renders with no controls at all
-- correctly, since the server would refuse. The consequence is that an orphaned row has **no
delete affordance anywhere and produces no error to notice**. It is not a broken button; it is
a row that quietly cannot be acted on, in a list that only grows. Nothing in the product ever
says why.

**This is a specified rule with an unconsidered consequence, not an oversight.**
`itemtemplates_test.go:42-48` states it and asserts it:

```go
// ...but only the owner may update or delete it.
if _, err := svc.UpdateItemTemplate(ctx, shared.ID, shared, "eve@example.org"); !errors.Is(err, batch.ErrForbidden) {
if err := svc.DeleteItemTemplate(ctx, "eve@example.org", shared.ID); !errors.Is(err, batch.ErrForbidden) {
```

`eve` is a non-owner of `amy`'s **shared** template and is refused. `batch_test.go:279` does
the same for macros. The rule is deliberate, tested, and right for a *personal* record. What
no test covers is the two cases where it produces an unreachable row: an **admin** acting on a
shared record, and a shared record whose **owner no longer exists**.

## Expected

- **Let an admin write any shared record.** Thread the caller's roles into `owned.go` and
  permit `update`/`delete` when `cm.Owner == owner` **or** the caller is an admin *and* the
  record is `Shared`. Personal records stay private to their owner, which is the property the
  current rule was protecting. A shared record is library property; its custodian should be the
  library.

- **Decide what a deleted user's shared records become.** `DeleteUser` should, at minimum,
  refuse to orphan them (409, listing what must be reassigned) or reassign them to the deleting
  admin. Silently leaving them is the one option that produces an unreachable record. A
  `POST /v1/macros/{id}/reassign` would also close it.

- **Do not compare owners by a recyclable string.** Ownership keyed on the email address means
  re-creating an account grants control of the old account's shared records. A stable user id
  would fix that; if the address must stay the key, say so where `OwnedMeta.Owner` is
  documented, because today it reads as an identity and behaves as an address.

- **Once an admin may act, `Macros.svelte:189` must let them.** Its guard is `m.owner === me`,
  which is exactly right today. It becomes wrong the moment the server grows an override, so
  the two changes belong in one commit.

- **Test the two cases the current tests skip.** `itemtemplates_test.go:42-48` and
  `batch_test.go:279` already drive two principals and assert `ErrForbidden` for a non-owner on
  a shared record -- so a fix here is a **deliberate change to a specified rule**, and those
  assertions must be rewritten rather than merely kept passing. What is missing is a third
  actor (an admin, expected to succeed on a shared record and still be refused on a personal
  one) and a shared record whose owner has been deleted.

## Repro

```bash
cd ~/libcat-e2e && node harness/probe_owned_shared.mjs   # O3, O4, O5, O6
cd ~/libcat-e2e && node harness/retest.mjs               # check t292
```

Read/write against the playground on `:8481` only. Every record it creates is labelled
`zz-e2e-…`, and it cleans up after itself by the only route that exists -- recreating the
sentinel owner's account to delete the rows, then removing the account.

Its controls carry the argument. `O0` shows both routes answer and the sentinel librarian
exists. **`O1` shows the owner CAN delete their own shared record (204)** -- so the server's
lifecycle is complete and 403 below is a policy, not a broken route. **`O2` shows a second
librarian sees the shared record in their list** -- so it really is library-wide, and "nobody
can delete it" is a statement about something everybody can see.

By hand, against `:8481`:

```bash
# as a plain librarian B
curl -s -XPOST -H "Authorization: Bearer $B" -H 'content-type: application/json' \
  -d '{"label":"zz-e2e","shared":true}' localhost:8481/v1/item-templates       # 201, owner=B

# as the admin A
curl -s -o /dev/null -w '%{http_code}\n' -XDELETE -H "Authorization: Bearer $A" \
  localhost:8481/v1/item-templates/$ID                                          # 403 not the owner
curl -s -o /dev/null -w '%{http_code}\n' -XDELETE -H "Authorization: Bearer $A" \
  localhost:8481/v1/users/$B_EMAIL                                              # 204
curl -s -H "Authorization: Bearer $A" localhost:8481/v1/item-templates          # still there
```

## Outcome

Shipped in **libcat v0.142.0** (minor -- the probe has something to adopt: O3-O6
flip from 403 to success). Took the first fork from Expected: give an admin
custody of shared records, rather than removing the feature.

**Server (`batch/owned.go`).** A new `writable(meta, owner, isAdmin)` gates both
`updateOwned` and `deleteOwned`: the owner always may write, and an admin may
write a record that is `Shared`. A personal record stays private to its owner
even from an admin -- and in fact is invisible to everyone but its owner, so an
admin acting on someone else's personal record gets `NotFound`, not a policy
refusal. An admin editing a colleague's shared record is its custodian, not its
owner: `updateOwned` preserves the original `Owner` and refuses to let a
non-owner un-share it (which would move it into the departed owner's partition
and re-orphan it). The service methods (`UpdateMacro`/`DeleteMacro`,
`UpdateItemTemplate`/`DeleteItemTemplate`) thread an `isAdmin bool`; the handlers
pass `id.CanAdmin()`.

**UI.** `Macros.svelte` and `ItemsPanel.svelte` replace their `owner === me`
guard with a `canManage` that also admits an admin on a shared record, so the
custodian controls the server now honours actually render. The macro editor
disables the "Shared" toggle when an admin edits a record they do not own (the
server ignores an un-share there), and the 403 messages now name the admin path.

**Tests.** The two "specified rule" tests (`itemtemplates_test.go`,
`batch_test.go`) are rewritten per Expected: the non-owner rule is kept, and the
two cases they skipped are added -- an admin succeeding on a shared record (and
still blocked from a colleague's personal one), and cleaning up a record whose
owner is gone.

**Deferred to tasks/332 (bullets 2 and 3).** `DeleteUser` still silently orphans
(now admin-*reachable* but not surfaced), and ownership is still keyed on a
recyclable email. Both are larger than this CRUD-guard change and are filed
together; 292's headline -- "deleted by nobody, not even an admin" -- is fully
resolved because an orphan is now admin-deletable.

### Verified end to end on :8481

Two real accounts (admin `eve`, throwaway librarian sentinel):

```
sentinel POST /v1/item-templates {shared:true}  -> 201  owner=sentinel
admin    GET  /v1/item-templates                 -> shared template listed
admin    PUT  /v1/item-templates/{id}            -> 200  relabelled, still shared, owner=sentinel
admin    DELETE /v1/item-templates/{id}          -> 204  (was 403)
sentinel POST /v1/macros {shared:true}           -> 201  owner=sentinel
admin    DELETE /v1/users/sentinel               -> 204
admin    GET  /v1/macros                          -> orphaned shared macro still listed
admin    DELETE /v1/macros/{id}                   -> 204  NOBODY-can-delete is fixed
```

And the real UI (Playwright), signed in as the admin: selecting a shared template
owned by another librarian in the Items panel shows **Rename** and **Delete**, and
clicking Delete removes it server-side. Backend `go test ./...` green; full UI
suite 323/323; `npm run check` clean.
