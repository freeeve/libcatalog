# 261 -- attachments are written grain-first: a failed byte Put leaves a phantom entry that lists but 404s on download, and DELETE discards its bs.Delete error so a failed removal reports 204 while the bytes stay on disk

Filed from libcat-e2e on 2026-07-09 (cross-repo ask).

Both of these were predicted from the source months ago and recorded in
`libcat-e2e/ADMIN_FEATURES.md` as unprobeable: *"Both need an induced storage failure
to observe, which the playground gives no handle for."* The playground gives no handle;
an instance of our own does. Booting a writable `lcatd` against a throwaway APFS clone
and `chmod a-w`-ing **only** the attachment subtree makes the blob store fail while the
grain store keeps working. Both predictions hold.

## Symptom

Two stores, one request, no ordering discipline.

**The phantom.** A staff attachment upload whose bytes cannot be stored returns 500 --
and the record now claims the attachment exists:

```
control: happy path            POST -> 201; list=["zz-e2e-phantom.txt"]; GET bytes -> 200
control: delete restores       list=[]
control: grain still writable  a plain tag edit still saves (only the bytes are blocked)

POST /v1/works/{id}/attachments?name=zz-e2e-phantom.txt
  -> 500 {"error":"attachment store failed"}

GET  /v1/works/{id}/attachments               -> ["zz-e2e-phantom.txt"]   <- listed
GET  /v1/works/{id}/attachments/zz-e2e-...    -> 404 "no such attachment"  <- no bytes
```

The third control matters: the grain store lives at `data/works/<hash-shard>/<id>.nq`
and the bytes at `data/attachments/<id[:2]>/<id>/<seg>`, so only the latter was made
unwritable. A tag edit saved fine throughout. The 500 is the byte write, and the grain
statement that precedes it survives it.

**The mirror.** Deleting an attachment whose bytes cannot be removed reports success:

```
DELETE /v1/works/{id}/attachments/zz-e2e-phantom.txt
  -> 204

statement removed from the grain : true
bytes still on disk              : true   (data/attachments/w0/w00jsjpd0e6s3q/ still holds the file)
```

The librarian is told the attachment is gone. The file is not.

## Root cause

`backend/httpapi/attachment_handlers.go:90-105` -- grain first, bytes second, no
compensation if the second fails:

```go
// Grain first: the describes-guard means a typo'd id never stores
// orphan bytes (the tasks/215 covers discipline).
etag, err := mutateWorkGrain(r, bs, ix, workID, func(g []byte) ([]byte, error) {
	return bibframe.SetAttachment(g, workID, name, true)
})
if err != nil {
	writeMutateError(w, err)
	return
}
if _, err := bs.Put(r.Context(), path, data, blob.PutOptions{}); err != nil {
	writeError(w, http.StatusInternalServerError, "attachment store failed")
	return          // <- the grain statement stays
}
```

The comment is right about what grain-first buys (a typo'd work id never orphans bytes)
and silent about what it costs. The `return` abandons a committed grain write.

Worse, `mutateWorkGrain` (`records_handlers.go:480-505`) has already called `ix.Apply`,
so the phantom is in the work index too, and the `ATTACHMENT_ADD` audit entry is written
*after* `bs.Put` (`attachment_handlers.go:105-109`) -- so the phantom exists in the grain
and the index with **no audit entry at all**. Nothing records that it was ever attempted.

`backend/httpapi/attachment_handlers.go:136-145` is the mirror, and here the error is
not even observed:

```go
etag, err := mutateWorkGrain(r, bs, ix, workID, func(g []byte) ([]byte, error) {
	return bibframe.SetAttachment(g, workID, name, false)
})
if err != nil {
	writeMutateError(w, err)
	return
}
if path, err := bibframe.AttachmentBlobPath(workID, name); err == nil {
	_ = bs.Delete(r.Context(), path)          // <- discarded
}
if legacy := bibframe.LegacyAttachmentBlobPath(workID, name); legacy != "" {
	_ = bs.Delete(r.Context(), legacy)        // <- discarded
}
```

Both `_ =` assignments throw away the only signal that the bytes survived.

## Why it matters

A blob `Put` or `Delete` failing is not exotic. `LCATD_S3_BUCKET` is a supported backend
(`config.go:164`); S3 throttles, returns 503, and rejects on expired credentials or a
tightened bucket policy. A directory backend fills up, or loses its mount. The failure
modes here are the ordinary ones.

**The phantom is a trap the cataloger cannot escape by repeating themselves.** They
upload a scan, see a server error, and try again. The second POST now finds the name in
the grain and answers `409 "zz-e2e-phantom.txt is already attached; delete it first, or
POST with ?replace=true"` (`:79-88`). The record lists an attachment that will not open,
and offering it again is refused because it is already there. Recovery requires deleting
a thing that never existed. And because the audit entry is written after the failed Put,
there is no trail saying anyone ever tried.

**The silent delete is the one with teeth.** Staff attachments are the private side of a
record -- rights correspondence, a donor letter, a scan of a damaged page. `DELETE`
answering `204` is a promise that the bytes are gone. When `bs.Delete` fails, they are
not, and nothing anywhere says so: not the response, not the audit entry (which is
written regardless), not the listing. The record stops pointing at the file, so no
subsequent operation will ever look at it again, and no cleanup pass will find it,
because the grain is the only index of what exists. A librarian who deletes an
attachment for a legal reason has been told they succeeded.

Neither failure corrupts a record. What both do is make libcat's report of its own state
untrue in the exact moment the operator most needs it to be true.

## Expected

- **Compensate the failed upload.** If `bs.Put` fails, undo the grain statement before
  returning -- `SetAttachment(g, workID, name, false)` through `mutateWorkGrain` again --
  and report 500 only after. A best-effort rollback that itself fails is worth an
  `ERROR`-level log and a distinct message (*"the attachment was recorded but its bytes
  were not stored; delete and retry"*), which is still infinitely better than a silent
  phantom. Alternatively write the bytes first under a content-addressed path and let the
  grain statement be the commit point -- that inverts the failure into orphan bytes with
  no record, which is the cheaper mistake, and is what `tasks/215`'s describes-guard was
  protecting against in the first place.
- **Do not discard `bs.Delete`'s error.** Report it. A `DELETE` that removed the
  statement but not the bytes is not a 204; it is a 500 (or a 207-shaped body) that says
  which half succeeded. If a best-effort delete really is the intent -- because the grain
  is authoritative and the bytes are garbage-collectable -- then say so in a comment,
  log the failure, and add the sweeper that makes it true. Today there is no sweeper: the
  grain is the only listing.
- Consider whether the `ATTACHMENT_ADD` audit entry belongs *before* the byte write, so
  an attempt that fails half way is at least attributable (compare **259**, where the
  configuration surface writes no audit at all).

## Repro

```bash
cd ~/libcat-e2e && node harness/probe_attach_failure.mjs   # A4, A5, A6, A7
cd ~/libcat-e2e && node harness/retest.mjs                 # check t261
```

The probe never addresses :8481 or :8501. It builds `backend/cmd/lcatd`, makes an APFS
clone (`cp -Rc`, copy-on-write, instantaneous) of the playground's site directory, boots
a **writable** instance against the clone on :8478, and deletes the clone afterwards --
deleting a copy-on-write clone cannot touch the source. Its controls carry the argument:
`A1` proves upload/list/download all work on this instance, `A2` proves delete works, and
`A2b` proves the induced failure is *targeted* -- a plain tag edit still saves after the
chmod, so the grain store is demonstrably still writable and the 500 is the byte write
alone.

By hand:

```bash
go build -o /tmp/lcatd-rw ./backend/cmd/lcatd
cp -Rc ~/libcat-playground/site /tmp/site-rw
LCATD_LISTEN_ADDR=:8478 LCATD_BLOB_DIR=/tmp/site-rw LCATD_LOCAL_AUTH=1 \
  LCATD_BOOTSTRAP_ADMIN="ro@example.org:changeme123" \
  LCATD_ABUSE_SECRET=0123456789abcdef0123456789abcdef /tmp/lcatd-rw &

TOK=$(curl -s -XPOST -H 'Content-Type: application/json' \
  -d '{"email":"ro@example.org","password":"changeme123"}' localhost:8478/v1/auth/login | jq -r .accessToken)
W=$(curl -s -H "Authorization: Bearer $TOK" 'localhost:8478/v1/works?limit=1' | jq -r '.works[0].WorkID')

mkdir -p /tmp/site-rw/data/attachments/${W:0:2}/$W
chmod -R a-w /tmp/site-rw/data/attachments/${W:0:2}/$W

printf 'bytes\n' | curl -s -XPOST -H "Authorization: Bearer $TOK" --data-binary @- \
  "localhost:8478/v1/works/$W/attachments?name=zz.txt"
# {"error":"attachment store failed"}   500

curl -s -H "Authorization: Bearer $TOK" localhost:8478/v1/works/$W/attachments
# ["zz.txt"]                            <- the phantom

curl -s -o /dev/null -w '%{http_code}\n' -H "Authorization: Bearer $TOK" \
  localhost:8478/v1/works/$W/attachments/zz.txt
# 404

chmod -R u+w /tmp/site-rw/data/attachments/${W:0:2}/$W && rm -rf /tmp/site-rw
```

For the mirror: upload successfully, then `chmod -R a-w` the same directory and
`DELETE` the attachment. It answers `204`; the file is still there.
