# 266 -- cover art repeats 261's grain-first shape: a failed byte Put leaves a phantom cover and no COVER_SET audit, and DELETE discards sweepStaleCovers' errors so a takedown answers 204 while the image keeps serving at its public unauthenticated URL

Filed from libcat-e2e on 2026-07-09 (cross-repo ask).

**261 was fixed in `9956600` for `attachment_handlers.go` only.** `cover_handlers.go`
carries both halves of the same defect, and its delete half is worse, because a cover's
bytes are served from a **public, unauthenticated, guessable URL**.

## Symptom

Measured on a throwaway writable clone (`:8474`, never :8481 or :8501) with `chmod a-w`
on one cover shard, `data/covers/<id[:2]>/`. The grain lives in
`data/works/<hash-shard>/` and stays writable, so the blob store fails and the grain
store does not.

```
control: happy path         PUT -> 200; GET /covers/w00…q.png -> 200; grain cover=covers/w00…q.png
control: delete restores    DELETE -> 204; GET -> 404; bytes on disk=false
control: grain writable     a plain tag edit still saves after the chmod (200, with If-Match)
control: audit log open     COVER_SET on an unaffected work -> 200; audit 3 -> 4

PUT /v1/works/{id}/cover  (image/png, valid bytes)
  -> 500 {"error":"cover store failed"}

GET /v1/works/{id}/doc  .cover  -> "covers/w00…q.png"   <- the record claims a cover
GET /covers/w00…q.png            -> 404                  <- there are no bytes
COVER_SET audit entries written  -> 0                    <- and nothing records the attempt
```

The mirror, and the one with teeth:

```
(restage a real cover, then chmod the shard unwritable)

DELETE /v1/works/{id}/cover
  -> 204

bytes still on disk               : true
GET /covers/w00…q.png             : 200      <- public, unauthenticated, still downloadable
grain still references a cover    : ""       <- nothing will ever collect the bytes
```

The librarian is told the cover is gone. It is still being served.

## Root cause

`backend/httpapi/cover_handlers.go:116-127` -- grain first, bytes second, no
compensation, and the audit entry after both:

```go
url := "covers/" + workID + "." + ext
// Grain first: SetCover verifies the work exists, so a typo'd id
// never stores orphan bytes.
etag, err := mutateWorkGrain(r, bs, ix, workID, func(g []byte) ([]byte, error) {
	return bibframe.SetCover(g, workID, url)
})
if err != nil {
	writeMutateError(w, err)
	return
}
if _, err := bs.Put(r.Context(), bibframe.CoverBlobPath(workID, ext), data, blob.PutOptions{}); err != nil {
	writeError(w, http.StatusInternalServerError, "cover store failed")
	return          // <- the grain statement stays, and WriteAudit below never runs
}
sweepStaleCovers(r, bs, workID, ext)
if queue != nil {
	queue.WriteAudit(r.Context(), suggest.AuditEntry{
		WorkID: workID, Action: "COVER_SET", Actor: id.Email, ETag: etag, Note: url,
	})
}
```

This is `attachment_handlers.go:90-105` before `9956600`, comment and all. Because
`mutateWorkGrain` has already called `ix.Apply`, the phantom is in the work index too.

`backend/httpapi/cover_handlers.go:61-68` is the delete half, and here the error is not
even observed:

```go
func sweepStaleCovers(r *http.Request, bs blob.Store, workID, keep string) {
	for _, ext := range coverExts {
		if ext == keep {
			continue
		}
		_ = bs.Delete(r.Context(), bibframe.CoverBlobPath(workID, ext))   // <- discarded
	}
}
```

`DELETE` calls it as `sweepStaleCovers(r, bs, workID, "")` (`:151`), so all three
`bs.Delete` errors are thrown away and the handler answers `204` regardless.

The function's own doc comment (`:48-59`) names the exact failure it then fails to
report:

> Replacing a JPEG with a PNG repointed the grain and left the JPEG serving from its
> public, unauthenticated, guessable URL forever -- nothing referenced it, so nothing
> would ever collect it. A cataloger replaces a cover precisely when the old one is
> wrong: wrong edition, rights complaint, an image that should not have been published.
> **A takedown that looks done was not done (tasks/243).**

tasks/243 fixed the *ordering* bug that stranded the old format's bytes. It did not make
the sweep report a failure, so the identical outcome returns whenever `bs.Delete` errors
rather than whenever the extension changes.

`GET /covers/{file}` (`:161`) is deliberately public -- *"covers are display assets the
static site republishes anyway"* -- and `CoverBlobPath` is
`data/covers/<workID[:2]>/<workID>.<ext>` (`bibframe/cover.go:16`), so the serving URL is
`covers/<workID>.<ext>`: derivable from any work id the OPAC already exposes.

## Why it matters

**The silent delete is a failed takedown, and covers are the one asset where that is a
legal problem rather than a hygiene problem.** Attachments (261) are staff-only: when
`bs.Delete` failed there, the bytes became unreachable because the grain was the only
index. A cover's bytes have their own public route. A librarian removing a cover for a
rights complaint, a DMCA notice, or because it is the wrong edition gets `204`, sees the
cover disappear from the record, and the image is still downloadable by anyone who can
construct the URL -- forever, because `SetCover(g, workID, "")` has just removed the only
reference anything would ever use to find it again. There is no sweeper.

**The phantom is a trap the cataloger cannot escape by retrying.** After the 500 the
record claims `covers/<id>.png`. The OPAC's cover slot and the editor's Cover panel both
render that URL (`records_handlers.go:73` returns it precisely so the panel can "show
what the record has and offer Remove"), and it 404s. Re-uploading hits the same failing
`bs.Put`. Because `WriteAudit` runs only after `bs.Put` succeeds, **no `COVER_SET` entry
is written at all** -- so the audit log, which does record `COVER_SET` on success
(`:130`), has no trace that anyone ever tried. That is the same asymmetry **259** is
about, arrived at from the other direction: here the action is audited, but only when it
works.

A blob `Put` or `Delete` failing is ordinary. `LCATD_S3_BUCKET` is supported
(`config.go:164`); S3 throttles, 503s, and rejects on expired credentials or a tightened
bucket policy. A directory backend fills up or loses its mount.

Neither failure corrupts a record. Both make libcat's report of its own state untrue at
the moment an operator most needs it to be true -- and one of them makes a takedown a lie.

## Expected

The fix `9956600` applied to attachments is the fix here, plus one more for the sweep.

- **Compensate the failed upload.** If `bs.Put` fails, undo the grain statement --
  `SetCover(g, workID, "")` through `mutateWorkGrain` again -- and report 500 only after.
  A rollback that itself fails deserves an `ERROR`-level log and a distinct message
  (*"the cover was recorded but its bytes were not stored; remove it and retry"*).
  Alternatively store the bytes first: the grain statement then becomes the commit point,
  and the failure mode inverts to orphan bytes with no record, which is the cheaper
  mistake. Note the `sniffCover` guard already rejects a mistyped payload before either
  store is touched, so "grain first" is buying less here than its comment claims -- the
  work-existence check is the only part `SetCover` contributes.
- **Do not discard `bs.Delete`'s error in `sweepStaleCovers`.** Return it. A `DELETE`
  that removed the statement but not the bytes is not a `204`. Given the public route, it
  should be a 500 that says the bytes survived, so the operator knows the takedown did not
  happen. If a best-effort sweep really is the intent for the *replace* path (where the
  new cover is already serving), then split the two callers: `PUT` may log-and-continue,
  `DELETE` must not.
- **Write the `COVER_SET` audit entry before the byte write,** or write a
  `COVER_SET_FAILED` entry on the error path. Today a failed cover upload is invisible to
  the audit log. (Compare **259**.)
- **Sweep for orphans.** Whichever way the ordering goes, `data/covers/` has no
  reconciliation pass against the grains. `lcat export -covers` walks records, not blobs,
  so an orphan is never noticed. That is what makes a discarded `Delete` permanent rather
  than merely late.

## Repro

```bash
cd ~/libcat-e2e && node harness/probe_cover_failure.mjs   # C4a, C4b, C4c, C5a, C5b
cd ~/libcat-e2e && node harness/retest.mjs                # check t266
```

The probe never addresses :8481 or :8501. It builds `backend/cmd/lcatd`, makes an APFS
clone (`cp -Rc`, copy-on-write) of the playground's site, boots a writable instance on
:8474, and deletes the clone afterwards. Its controls carry the argument: `C1`/`C2` prove
PUT and DELETE both work on this instance, `C3` proves the induced failure is *targeted*
(a plain tag edit still saves with `If-Match` after the chmod, so the grain store is
demonstrably still writable and the 500 is the byte write alone), `C7` brackets the
missing audit entry with a `COVER_SET` that the log does accept, and `C5c` shows the grain
really did drop its reference, so nothing will ever look for the surviving bytes again.

By hand:

```bash
go build -o /tmp/lcatd-rw ./backend/cmd/lcatd
cp -Rc ~/libcat-playground/site /tmp/site-rw
LCATD_LISTEN_ADDR=:8474 LCATD_BLOB_DIR=/tmp/site-rw LCATD_LOCAL_AUTH=1 \
  LCATD_BOOTSTRAP_ADMIN="ro@example.org:changeme123" \
  LCATD_ABUSE_SECRET=0123456789abcdef0123456789abcdef /tmp/lcatd-rw &

TOK=$(curl -s -XPOST -H 'Content-Type: application/json' \
  -d '{"email":"ro@example.org","password":"changeme123"}' localhost:8474/v1/auth/login | jq -r .accessToken)
W=$(curl -s -H "Authorization: Bearer $TOK" 'localhost:8474/v1/works?limit=1' | jq -r '.works[0].WorkID')

# stage a real cover, then make only the cover shard unwritable
curl -s -XPUT -H "Authorization: Bearer $TOK" -H 'Content-Type: image/png' \
  --data-binary @cover.png "localhost:8474/v1/works/$W/cover"
chmod -R a-w /tmp/site-rw/data/covers/${W:0:2}

curl -s -XDELETE -H "Authorization: Bearer $TOK" -o /dev/null -w '%{http_code}\n' \
  "localhost:8474/v1/works/$W/cover"
# 204   <- "the cover is gone"

curl -s -o /dev/null -w '%{http_code}\n' "localhost:8474/covers/$W.png"
# 200   <- no token; the cover is still public

chmod -R u+w /tmp/site-rw && rm -rf /tmp/site-rw
```

For the phantom: `chmod -R a-w` the shard of a work with **no** cover, then `PUT` one. It
answers `500 {"error":"cover store failed"}`, `GET /v1/works/{id}/doc` reports
`cover: "covers/<id>.png"`, and `GET /covers/<id>.png` answers 404.
