# 260 -- in read-only mode the allowlisted `ops` and `marc` execute paths answer 500 "grain write failed" instead of the guard's 403, and MarcPanel still renders an ungated "Save MARC" button

Filed from libcat-e2e on 2026-07-09 (cross-repo ask).

**Nothing persists.** The read-only blob store does its job: after every call below the
grain etag is unchanged, no tag is added, and no audit entry is written. What is wrong is
the answer the server gives, and a button the SPA should not be showing.

## Symptom

`readOnlyGuard`'s own doc comment (`backend/httpapi/readonly.go:20`) promises:

> Grain and blob-backed config writes are double-covered by the read-only blob store;
> **this guard adds clean 403s** and blocks the editorial writes that live in the
> document store.

Measured against a purpose-built `LCATD_READ_ONLY=1` instance (not :8481, not :8501 --
see Repro). One condition, four different answers:

```
POST /v1/vocabsources          (blocked route)  -> 403 {"error":"read-only demo: changes are not saved"}   <- control
POST /v1/works/{id}/ops        (dryRun:true)    -> 200, 1 added quad in the diff                           <- control

POST /v1/works/{id}/ops        (execute)        -> 500 {"error":"grain write failed"}
POST /v1/works/{id}/marc       (execute)        -> 500 {"error":"grain write failed"}
POST /v1/batch/ops             (execute)        -> 200 {"applied":0,"failed":1,
                                                        "results":[{"error":"blob: store is read-only"}]}
```

And nothing was written -- the part that actually matters holds:

```
etag unchanged; tags=[]        (before and after all three execute calls)
audit entries in this month: 0
```

`/v1/batch/ops` is the one that gets it right: it reports the true cause, per item,
without pretending the server broke.

## Root cause

Two independent gaps.

**1. `blob.ErrReadOnly` is matched nowhere.** `storage/blob/readonly.go:26`:

```go
func (readOnly) Put(context.Context, string, []byte, PutOptions) (string, error) {
	return "", ErrReadOnly
}
```

`backend/httpapi/records_handlers.go:222-237` handles exactly one sentinel and buckets
the rest as a server fault:

```go
newTag, err := bs.Put(r.Context(), bibframe.GrainPath(workID), updated, blob.PutOptions{...})
if errors.Is(err, blob.ErrPreconditionFailed) {
	... 412 ...
}
if err != nil {
	writeError(w, http.StatusInternalServerError, "grain write failed")
	return
}
```

`backend/httpapi/marc_handlers.go:132,145` is the same code shape. A repo-wide grep for
`blob.ErrReadOnly` outside its own package returns **nothing** -- the sentinel is
exported and never consulted.

This only surfaces on the two routes `readOnlyAllowed` deliberately lets through
(`readonly.go:53-57`, `HasSuffix(path, "/ops")` and `"/marc"`), on the strength of the
comment at `:14-16`: *"their execute path is separately blocked at the read-only blob
store"*. It is blocked -- as a 500.

**2. `MarcPanel` never asks whether the instance is read-only.**
`backend/ui/src/components/SaveBar.svelte:20-37` is careful:

```svelte
// In the read-only demo, saving is disabled but Preview still shows the diff.
// In the sandbox demo, Save is shown but renders the edit without persisting.
{#if sandbox}
  <button ... title="Renders the edit in the demo; not saved">Save (demo)</button>
{:else if !readOnly}
  <button ... onclick={onsave}>Save</button>
{/if}
```

`backend/ui/src/components/MarcPanel.svelte:156` is not -- the file contains no
reference to `isReadOnly`, `isSandbox`, or `readOnly` at all:

```svelte
<button class="button" onclick={() => void save()} disabled={busy || blocked}>{busy ? "Working…" : "Save MARC"}</button>
```

and its `save()` (`:76-81`) posts the **execute** path:

```ts
const res = await postMarc(workId, active, $state.snapshot(records[active]), { ifMatch: etag });
```

So the 500 is reachable from the shipped UI: open a record in a read-only demo, switch
to the MARC tab, edit a subfield, click **Save MARC**. (`WorkEditor`'s own Save is
correctly hidden, and in sandbox mode `editor.ts:221` routes Save through `sandboxSave`
-> `dryRun`. The MARC panel bypasses both.)

## Why it matters

Read-only is not a corner: `LCATD_READ_ONLY=1` is the demo deployment mode, and
`LCATD_SANDBOX=1` implies it (`config.go:194`, *"sandbox never persists"*). These are the
instances strangers touch.

- **A 5xx is a claim that the server is broken.** A read-only demo returns 500 on an
  ordinary user action, so its error rate is whatever fraction of visitors click Save
  MARC. Any alerting on 5xx pages someone; any client retry loop retries a request that
  can never succeed. `429`/`403` are throttles a client understands; `500` is "try
  again, this is our fault."
- **The message is wrong twice over.** *"grain write failed"* names a mechanism the
  caller cannot see and implies data loss. The truth -- *"read-only demo: changes are
  not saved"* -- is a string that already exists eight lines away in `readonly.go:26`,
  and `blob.ErrReadOnly` already carries it (`"blob: store is read-only"`).
- **The MARC panel offers a control that cannot work.** A cataloger evaluating libcat on
  the demo edits a `245`, clicks Save MARC, and gets a server error. The panel that
  should have shown them *"Preview delta"* and a disabled Save instead shows them a
  crash. `SaveBar` proves the team already decided what this should look like.

The blast radius is bounded and worth saying plainly: **no data is at risk.** The blob
store refuses the write, and the handler returns before `ix.Apply`, `AppendFeed` and
`WriteAudit` (`records_handlers.go:238-243`), so the index, the feed and the audit trail
stay clean. This is an error-contract and UI-gating defect, not a persistence one.

## Expected

- **Map the sentinel.** In `records_handlers.go` and `marc_handlers.go`, before the
  generic 500:

  ```go
  if errors.Is(err, blob.ErrReadOnly) {
      writeError(w, http.StatusForbidden, "read-only demo: changes are not saved")
      return
  }
  ```

  Same string the guard uses, so the two paths are indistinguishable to a client. If a
  shared helper is preferable, `writeBlobError(w, err)` would cover the
  `ErrPreconditionFailed` case too, which is duplicated between the two files today.
- **Gate `MarcPanel` the way `SaveBar` is gated.** Hide **Save MARC** when `isReadOnly()`
  and not `isSandbox()`; in sandbox, either hide it or make it call `postMarc(..., {dryRun:
  true})` and render the returned diff, matching `sandboxSave`. **Preview delta** should
  stay in both cases -- that is the whole point of the allowlist.
- Consider whether `readOnlyAllowed`'s **suffix** matching is what you want.
  `strings.HasSuffix(path, "/ops")` and `"/marc"` allow any future route ending in those
  words through the guard, relying entirely on the blob store to catch it. A route that
  wrote to the *document* store would not be caught -- `appdeps.go:94-96` wraps only the
  blob:

  ```go
  if cfg.ReadOnly && deps.Blob != nil {
      deps.Blob = blob.ReadOnly(deps.Blob)
      deps.ReadOnly = true
  }
  ```

  Matching on the routed pattern, or an explicit allowlist of the three paths, would make
  that impossible rather than merely currently-true.

## Repro

```bash
cd ~/libcat-e2e && node harness/probe_readonly.mjs   # R3, R4, R6
cd ~/libcat-e2e && node harness/retest.mjs           # check t260
```

The probe never addresses :8481 or :8501. It builds `backend/cmd/lcatd`, makes an APFS
clone (`cp -Rc`, copy-on-write, instantaneous) of the playground's site directory, and
boots a read-only instance against the clone on :8479 -- so even a broken guard could
only write to a throwaway copy. Its controls are the load-bearing part: `R1` proves the
guard is active (a blocked route answers 403), `R2` proves the allowlisted dry run still
works, and `R7`/`R8` prove the record and the audit log are untouched afterwards.

By hand:

```bash
go build -o /tmp/lcatd-ro ./backend/cmd/lcatd
cp -Rc ~/libcat-playground/site /tmp/site-ro
LCATD_LISTEN_ADDR=:8479 LCATD_READ_ONLY=1 LCATD_BLOB_DIR=/tmp/site-ro \
  LCATD_LOCAL_AUTH=1 LCATD_BOOTSTRAP_ADMIN="ro@example.org:changeme123" \
  LCATD_ABUSE_SECRET=0123456789abcdef0123456789abcdef /tmp/lcatd-ro &

TOK=$(curl -s -XPOST -H 'Content-Type: application/json' \
  -d '{"email":"ro@example.org","password":"changeme123"}' localhost:8479/v1/auth/login | jq -r .accessToken)
W=$(curl -s -H "Authorization: Bearer $TOK" 'localhost:8479/v1/works?limit=1' | jq -r '.works[0].WorkID')
ET=$(curl -s -H "Authorization: Bearer $TOK" "localhost:8479/v1/works/$W" | jq -r .etag)

# the guard, working:
curl -s -XPOST -H "Authorization: Bearer $TOK" -H 'Content-Type: application/json' \
  -d '{"name":"zz","scheme":"zz"}' localhost:8479/v1/vocabsources
# {"error":"read-only demo: changes are not saved"}   403

# the same condition, through an allowlisted route:
curl -s -XPOST -H "Authorization: Bearer $TOK" -H "If-Match: $ET" -H 'Content-Type: application/json' \
  -d '{"ops":[{"resource":"work","path":"tags","action":"add","value":{"v":"zz"}}]}' \
  localhost:8479/v1/works/$W/ops
# {"error":"grain write failed"}                      500
```

In the UI: open a record on a read-only instance, choose the **MARC** tab, change a
subfield, click **Save MARC**.

## Outcome

Fixed in **v0.108.0** (`0208541`). `probe_readonly.mjs` **9/9**; `retest.mjs`
**t260 FIXED**, nothing regressed.

Both halves held, and both suggested fixes were the right ones. The report's
framing -- *nothing persists; what is wrong is the answer the server gives* -- is
exactly the scope of the change.

### What shipped

**`writeGrainWriteError`** maps `blob.ErrReadOnly` onto the guard's own 403 and
its own wording, so a client cannot tell which layer refused it.

**`mutateWorkGrain` wrapped the store's error with `%v`.** That destroyed the
sentinel for *every* route that writes through it -- items, covers, relations,
attachments -- so they would all have 500'd the day the guard was relaxed or a
store was mounted read-only for another reason. It wraps with `%w` now, and
`writeMutateError` checks `ErrReadOnly` before `errGrainStore`: a deployment that
does not accept writes is not an unavailable one. The report only named the two
explicit `bs.Put` sites; this one was a level down.

**`readOnlyAllowed` no longer matches on suffix.** It matches the route's shape --
`/v1/works/{id}/<named suffix>` -- plus three exact paths. The report's third
bullet was right that the old form relied entirely on the blob store to catch a
future `/v1/queue/ops`, and that a route writing to the *document* store would
not have been caught at all.

I did **not** validate the work id inside the allowlist. A malformed id should
reach the handler and earn its 400 rather than be masked by the guard's 403.

**`MarcPanel` is gated the way `SaveBar` already was**: Save hidden in the
read-only demo, and in sandbox a `Save MARC (demo)` that dry-runs and renders the
delta without persisting, matching `editor.ts`'s `sandboxSave`. Preview delta
stays in both, which is why the guard allowlists the route at all.

### Every guard proven by mutation

Un-mapping the sentinel, restoring the `%v` wrap, restoring the suffix match, and
removing the panel's gate each make a specific named test fail. The `%v` mutation
is the one worth remembering: it compiles, it reads correctly, and it silently
breaks `errors.Is` two call frames away.

### Not in scope, deliberately

`/v1/batch/ops` was already right -- it reports the store's error per item inside
a 200, because a batch's per-entry results are its contract (compare tasks/268).
It is untouched.
