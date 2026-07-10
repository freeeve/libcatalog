# 272 -- batch ops shows the cataloger the server's raw store error

Filed from libcat-e2e on 2026-07-09 (cross-repo ask).

`batch.runOne` hands `publish.MutateGrain`'s error straight to the client, and
`BatchOps.svelte` renders it verbatim into the results list. A cataloger whose
batch run hits a store failure is shown an absolute filesystem path, a temp file
name, and a syscall error.

The comparison is the argument, not the string. **Twelve lines up in the same
function**, the read path maps its store errors to human text. **One route over**,
the single-record `POST /v1/works/{id}/ops` answers the identical induced failure
with `500 "grain write failed"`. **The sibling batch route**, `POST
/v1/covers/batch`, sets `res.Failed = "cover store failed"` (tasks/268). Three
precedents; the batch write path is the one place that does not hold the line.
This is **260**'s question -- what may a refusal tell a client -- asked of a
message body instead of a status code.

Grepping the shape rather than the file finds **two more client-facing call sites**
with the same unwrapped error: the authorities merge handler (which also answers
`409`) and the promotion-approve handler. See "the same shape, one grep away"
below.

Measured against **committed HEAD `359e112`** on a throwaway clone (`:8476`, never
:8481 or :8501), with `chmod a-w` on one work's grain shard.

## Symptom

```
control: both works apply while writable
  POST /v1/batch/ops {kind:ids, ids:[BROKEN, HEALTHY]}   -> 200 applied=2 failed=0

chmod -R a-w <site>/data/works/31/          # BROKEN's grain shard only

control: the healthy work still applies
  POST /v1/batch/ops {kind:ids, ids:[BROKEN, HEALTHY]}   -> 200 applied=1 failed=1

the failed record's message, as rendered to the cataloger:

  "open /var/folders/34/_z7403jx0bn7xgtss8vvfpnw0000gn/T/libcat-e2e-readonly/
   site-rw-8476/data/works/31/.blob-12955389: permission denied"

control: the READ path, in the same function
  POST /v1/batch/ops {ids:["w0000000nonexistent"]}       -> 200 "no such work"

control: the SINGLE-record route, same work, same op, same broken shard
  POST /v1/works/{BROKEN}/ops                            -> 500 "grain write failed"

control: nothing persisted on the broken work
  etag 72b5ac39... -> 72b5ac39...                        (unchanged)
```

The three controls are the finding. The same store error, reached three ways,
produces `"no such work"`, `"grain write failed"`, and an `*os.PathError`.

## Root cause

`backend/batch/batch.go:381-383` -- the write path returns the error unmapped:

```go
etag, err := publish.MutateGrain(ctx, s.Blob, t.path, func(old []byte) ([]byte, error) {
	...
})
if err != nil {
	item.Error = err.Error()     // <- raw
	return item
}
```

while `:353-356`, in the same function, does map it:

```go
grain, _, err := s.Blob.Get(ctx, t.path)
if err != nil {
	item.Error = readError(err)  // "no such work", never the raw err
	return item
}
```

`readError` (`batch.go:393`) exists for exactly this and is called on the read
side only.

`publish.MutateGrain` (`backend/publish/publisher.go:99-105`) returns `st.Put`'s
error unwrapped, and `blob.DirStore.PutStream` fails with an `*os.PathError`
naming the temp file it could not create. Two further leaks live on the same
path:

- `publisher.go:108` -- `fmt.Errorf("publish: %s: conditional write kept failing", path)`
  embeds the blob path, and reaches the client the same way after `casAttempts = 8`
  (`publisher.go:32`) lost conditional writes.
- `blob.ErrReadOnly` is `"blob: store is read-only"` (`storage/blob/readonly.go:9`),
  so in the read-only demo every batch execute reports the package's internal
  name per record. The UI hides the apply control there
  (`BatchOps.svelte:275,401,440`), so no visitor meets it -- but the route is
  allowlisted (tasks/260), so the API answers it.

`backend/ui/src/screens/BatchOps.svelte:422-425` renders it:

```svelte
<li class:failed={!!item.error}>
  {#if item.error}
    <span class="error">{item.error}</span>
```

with no `humanApiMessage()` -- the helper the same screen uses for the *request*
error at `:187,213`.

**The same shape, one grep away -- twice.** Grepping for the *shape* rather than the
file (`grep -rn "MutateGrain(" --include='*.go' .`) finds six call sites. Three of
them answer an HTTP request, and all three leak:

```go
// batch/batch.go:382                 -- rendered into the results list
item.Error = err.Error()

// httpapi/authorities_handlers.go:176 -- POST /v1/authorities/merge
case err != nil:
	writeError(w, http.StatusConflict, err.Error())

// httpapi/promotion_handlers.go:75    -- approving a tag promotion
writeError(w, http.StatusInternalServerError, "rewrite failed: "+err.Error())
```

The merge handler gets the status wrong on top of the message: `Merge` calls
`publish.MutateGrain` twice (`authoritiesvc/service.go:199,216`), so a store
failure answers `409 Conflict` -- a claim that somebody else edited the record --
with an `*os.PathError` as its body. The promotion handler concatenates the raw
error into a `500` and rewrites every work carrying the tag, so the leak lands on
the one operation that touches the most records at once.

**Read, not measured**: only `batch.runOne` was driven with an induced failure this
cycle. The other two are the same call, the same unwrapped error, and the same
`writeError`. The remaining call sites (`publish/publisher.go:135`,
`publish/promote.go:104`) run inside the publish job and do not answer a request.

## Why it matters

**It is useless to the person who reads it.** A cataloger who batch-tagged forty
records and sees `open .../.blob-12955389: permission denied` beside one of them
cannot act on it, cannot tell whether the record changed, and cannot tell whether
retrying is safe. `"grain write failed"` says the same amount and does not pretend
otherwise. The message the single-record route already gives is the right one.

**It discloses the deployment's filesystem.** Absolute paths, the blob root, the
shard layout, and the temp-file naming scheme go to anyone holding a librarian
token. Librarians are trusted with records, not with the server's internals, and
the tokens are not hard to come by in a library with student workers. This is not
a critical hole -- it is the kind of thing an error contract exists to prevent,
and libcat has an error contract.

**tasks/260 settled the principle.** Its whole point was that a client must not be
able to tell which layer refused it, because a client that can tell will learn to
treat one refusal differently from the other. `readOnlyNotice` is shared between
the guard and the store for that reason. A raw `*os.PathError` tells the client a
great deal about which layer refused it.

**Nothing is corrupted.** The grain was never written, `Index.Apply` and the audit
both run only on success, and the healthy record in the same batch applied
cleanly. This is a reporting defect, which is precisely why it will sit there.

## Expected

- **Map the write error the way the read path already does.** A `writeError(err)`
  beside `readError(err)` in `batch.go`, returning `"the record changed, retry"`
  for `blob.ErrPreconditionFailed`, `readOnlyNotice` for `blob.ErrReadOnly`, and
  `"grain write failed"` otherwise -- the string `writeMutateError` already picks
  for the single-record route. Then the same failure says the same thing whichever
  route reaches it.
- **Log the raw error server-side**, as `cover_batch.go:87-90` does for its own
  per-record failures (`slog.Error(..., "err", res.Failed, ...)`). The operator
  needs the path; the cataloger does not. Today nobody gets it: it is neither
  logged nor durable, only rendered.
- **`publisher.go:108` should not embed the blob path** in an error that reaches a
  client, or `MutateGrain` should wrap its store errors in a sentinel so callers
  can map without string-matching.
- **Fix the other two client-facing call sites in the same pass.**
  `authorities_handlers.go:176` -- a store failure is not a `409`, and `err.Error()`
  is not a message. `promotion_handlers.go:75` -- `"rewrite failed: "+err.Error()`
  concatenates the raw error into a `500`. Same defect, same fix; splitting them
  out would leave the grep dirty, which is how 261's fix left 266 and 268 live one
  file away.
- Consider whether `BatchOps.svelte:425` should route `item.error` through
  `humanApiMessage`, as the screen already does for request-level errors. That is
  defence in depth, not the fix -- the server should not be sending it.

## Repro

```bash
cd ~/libcat-e2e && node harness/probe_batch_write_failure.mjs   # W2
cd ~/libcat-e2e && node harness/retest.mjs                      # check t272
```

The probe never addresses :8481 or :8501 and never reads `~/libcat`'s working
tree: `roinstance.buildHead()` exports committed HEAD with `git archive` into a
scratch dir and builds `cmd/lcatd` there. It clones the playground's site
(`cp -Rc`, copy-on-write), boots a writable instance on :8476, restores the write
bits and deletes the clone afterwards.

Its controls carry the argument. `W0` applies to both works while the shard is
writable. `W1` shows the healthy work in the *same batch* still applies after the
chmod, so the failure is scoped to one shard rather than a broken instance. `W3`
shows the read path in the same function maps its store error. `W4` shows the
single-record route answers the identical induced failure with `500 "grain write
failed"` -- so the repo has an opinion about this string, expressed one route
away. `W5` shows the etag never moved, so this is a reporting defect and not a
phantom.

`W2` asserts a *property*, not the string this build happens to produce: the
message must not name a filesystem path, a syscall, or a temp file. A check keyed
on `"permission denied"` would pass vacuously the day the error text changes.

By hand:

```bash
SITE=...          # a writable clone's site dir
W=...             # a work whose grain lives in <SITE>/data/works/31/
TOK=...

chmod -R a-w "$SITE/data/works/31"
curl -s -XPOST -H "Authorization: Bearer $TOK" -H 'Content-Type: application/json' \
  -d '{"selection":{"kind":"ids","ids":["'$W'"]},"ops":[{"resource":"work","path":"tags","action":"add","value":{"v":"zz"}}]}' \
  localhost:8476/v1/batch/ops
# {"matched":1,"applied":0,"failed":1,"results":[{"workId":"...",
#   "error":"open /.../data/works/31/.blob-12955389: permission denied"}]}

curl -s -XPOST -H "Authorization: Bearer $TOK" -H 'Content-Type: application/json' \
  -H "If-Match: <etag>" -d '{"ops":[...]}' localhost:8476/v1/works/$W/ops
# {"error":"grain write failed"}      500 -- the same failure, one route away

chmod -R u+w "$SITE/data/works/31"
```
