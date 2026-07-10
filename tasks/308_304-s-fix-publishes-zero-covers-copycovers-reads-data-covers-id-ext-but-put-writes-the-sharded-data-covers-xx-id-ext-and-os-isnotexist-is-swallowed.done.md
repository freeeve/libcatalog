# 308 -- 304's fix publishes zero covers: copyCovers reads data/covers/&lt;id&gt;.ext but PUT writes the sharded data/covers/&lt;xx&gt;/&lt;id&gt;.ext, and os.IsNotExist is swallowed

Filed from libcat-e2e on 2026-07-10 (cross-repo ask).

A regression in `4bb220c` ("publish only the visible collection", tasks/304). The two
non-cover halves of 304 are genuinely fixed and verified -- `catalog.nq.gz` and
`catalog.mrc.gz` now carry only visible works. The cover half stopped working entirely.

`export/export.go:471-488`:

```go
for _, vg := range visible {
	if vg.cover == "" { continue }
	// The claim is a site-relative URL ("covers/<id>.<ext>"); the blob lives
	// under data/covers by the same base name.
	name := path.Base(vg.cover)
	data, err := os.ReadFile(filepath.Join(root, name))     // data/covers/<id>.<ext>
	if os.IsNotExist(err) {
		continue // the Work claims a cover the store no longer holds
	}
	…
}
```

The comment is the bug: the blob does **not** live under `data/covers` by the same base name.
`bibframe.CoverBlobPath` (`bibframe/cover.go:16-18`) shards it:

```go
func CoverBlobPath(workID, ext string) string {
	return "data/covers/" + workID[:min(2, len(workID))] + "/" + workID + "." + ext
}
```

so `PUT /v1/works/{id}/cover` writes `data/covers/w0/w0cfnsjg6micju.png`, while `copyCovers`
reads `data/covers/w0cfnsjg6micju.png`. The read always misses. `os.IsNotExist(err)` then
`continue`s -- **no error, no log line, no manifest entry**. Every cover in the store is
silently skipped.

The previous implementation walked the tree (`filepath.WalkDir(root, …)`) and flattened with
`filepath.Base(path)`, so the shard was invisible to it. Reading by constructed path made the
shard load-bearing, and the constructed path omits it.

## Symptom

Measured on a throwaway writable clone of the playground pinned to committed HEAD (`1e63e0e`),
driving the real `lcat serialize` / `project` / `export --covers-out` built from that HEAD.
Three sentinel works were given cover images through the real `PUT /v1/works/{id}/cover`; one
was suppressed, one tombstoned, one left visible.

```
covers/ after `lcat export --covers-out`:     0 files

the store holds:                              5 cover blobs
the visible sentinel claims:                  extra.cover = "covers/w49iq8dde0ig2m.png"
the blob is at:                               data/covers/w4/w49iq8dde0ig2m.png
copyCovers reads:                             data/covers/w49iq8dde0ig2m.png   -> ENOENT -> continue
```

`lcat covers --json` confirms the blobs are there and are not orphans: it scans 5 and reports
1 orphan (the tasks/243 stale-format residue), naming none of the sentinels.

Before `4bb220c`, the same probe on the same fixture published 5 covers.

### The 304 checks now pass for the wrong reason

This is what makes it worth filing rather than mentioning. tasks/304's evidence was:

```
covers/w1dh6vtir43o8i.png    the SUPPRESSED work's cover -- published
covers/w41iq8jmgsm1po.png    the TOMBSTONED work's cover -- published
```

Both are absent now. But so is the *visible* work's cover, and so is every other. **"The hidden
work's cover is not published" is trivially true of every id when nothing is published at all.**
`harness/probe_hidden_cover_published.mjs` carried `H1` -- *the visible work's cover IS
published, so covers are copied at all* -- precisely as the vacuity guard for `H4`/`H5`, and it
is the only check that failed. Without it the probe would have gone from 5/11 to 11/11 and
reported 304 fixed.

## Root cause

`export/export.go:477-478`. `path.Base(vg.cover)` discards the shard, and the store is sharded.

The escape hatch that hides it is `:479-481`: `os.IsNotExist(err) → continue`. That branch is
meant for *"the Work claims a cover the store no longer holds"* -- a real, tolerable condition
after tasks/243 residue is reaped. It cannot distinguish that from *"I looked in the wrong
place"*, so a path bug degrades to a silent no-op on every single work.

### Why the tests do not catch it

`export/visibility_test.go:107-113`:

```go
// plantCover writes a cover blob and the grain statement that claims it, the
// way PUT /v1/works/{id}/cover does.
func plantCover(t *testing.T, root, workID string) {
	dir := filepath.Join(root, "data", "covers")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, workID+".png"), []byte("PNG"+workID), 0o644)
	…
}
```

It writes the **unsharded** path, and its comment claims that is *"the way `PUT
/v1/works/{id}/cover` does"* it. `PUT` calls `bibframe.CoverBlobPath`. The fixture and the
production writer disagree, so `TestPublishesVisibleCover` asserts against a layout no store
ever has, and passes.

Nothing in `export/` calls `bibframe.CoverBlobPath` -- neither the code nor the fixture -- so
the one function that knows the layout is not consulted by either side of the test.

## Why it matters

**It is silent.** No error, no warning in the build log, no difference in the manifest (covers
have never had a manifest entry). `lcat build` reports success. The only symptom is that every
cover on the public site 404s.

**It looks exactly like a fixed bug.** tasks/285 established that no cover has *ever* rendered
from a `--covers-out` export, because the URL was document-relative; that was fixed in
`v0.116.x`. A deployment upgrading through `4bb220c` gets working cover URLs pointing at files
that are no longer copied. The two bugs cancel into "covers still don't work", and the second
one is invisible.

**Almost no deployment has local covers.** queerbooks' catalog carries **24,887 covers across
62,602 works, and every one of them is an absolute OverDrive CDN URL** (`extra.cover` starts
`https://`; measured, 0 relative). `copyCovers` only ever reads a blob for a *site-relative*
claim, so that deployment publishes no local covers either way and cannot tell the difference.
The only store that would notice is one with uploaded covers -- the playground, whose OPAC
nobody reads. This bug can live indefinitely.

## Expected

- **Use `bibframe.CoverBlobPath`.** The work id and the extension are both in hand:
  `vg.id` and `path.Ext(vg.cover)`. That is the function that defines the layout, and calling it
  makes the sharding impossible to get wrong twice.

- **Do not swallow the miss.** A visible Work that *claims* a cover and whose blob is absent is
  worth one line on `opts.Log`, the way `copyGzip`'s stale-blank-node warning is. Silence is
  what turned a path bug into an invisible one. If the intent is to tolerate reaped residue,
  count the misses and log the count.

- **Skip absolute cover URLs explicitly.** `vg.cover` may be `https://img1.od-cdn.com/…`, in
  which case `path.Base` yields `cover.JPG` and the code goes looking for a blob named after
  somebody else's CDN path. It currently `continue`s by accident (ENOENT). An explicit
  `if !strings.HasPrefix(vg.cover, "covers/") { continue }` says so.

- **Make `plantCover` write what `PUT` writes.** `filepath.Join(root, filepath.FromSlash(
  bibframe.CoverBlobPath(workID, "png")))`. Its comment already promises this. With that one
  change `TestPublishesVisibleCover` fails on the current code.

## Repro

```bash
cd ~/libcat-e2e && node harness/probe_hidden_cover_published.mjs   # H1 fails; H4/H5/H6/H7 report vacuous
cd ~/libcat-e2e && node harness/retest.mjs                         # check t308
```

**Touches neither `:8481` nor `:8501`.** It boots its own writable clone of the playground
pinned to committed HEAD, uploads three sentinel covers through the real `PUT`, and exports into
a scratch directory.

By hand, on any store with an uploaded cover:

```bash
find site/data/covers -type f          # data/covers/w0/w0cfnsjg6micju.jpg
lcat export --in site --out /tmp/x --covers-out /tmp/x/covers
ls /tmp/x/covers | wc -l               # 0
```

## Outcome

Fixed in **v0.126.0**, commit `823d339`. A regression I introduced in `4bb220c`,
correctly diagnosed here down to the swallowed `os.IsNotExist`.

`copyCovers` now asks `bibframe.CoverBlobPath(vg.id, ext)` -- the same function the
writer uses -- instead of constructing the path. Verified on a copy-on-write clone
of the playground, where exactly one **visible** work claims a cover:

```
                       v0.125.0   v0.126.0
covers published            0          1        covers/w0cfnsjg6micju.jpg
the grain's claim                      covers/w0cfnsjg6micju.jpg   (matches)
unclaimed .png sibling      -          withheld  (tasks/243 residue, still excluded)
```

A claimed cover the store no longer holds is still skipped -- that is real and
benign -- but it is now **counted and logged**:
`export: N of M claimed covers are not in the store and were not published`.
"Every cover is missing" and "one cover is missing" printed identically, which is
the whole reason this shipped.

### How I let this through, which is the part worth recording

Two failures, and neither was the code.

**The fixture agreed with the bug.** `plantCover` wrote `data/covers/<id>.png`,
the same flat path the broken reader read. So `TestCoversPublishOnlyVisibleWorks`
had a positive control -- "the visible Work's cover is published" -- and it passed,
because both halves were wrong in the same direction. It now plants through
`CoverBlobPath`, and `TestCoversAreReadFromTheShardedBlobPath` **fails if the flat
path exists at all**, so a reader that reads flat can never again be rescued by a
fixture that writes flat.

**I read my own evidence backwards.** The tasks/304 outcome quotes the real-store
comparison `covers published: 2 -> 0` and calls the 0 a success. Zero is what "no
hidden work ships a cover" looks like, and it is also what "no work ships a cover"
looks like. I never asked `catalog.json` which visible works claim one -- the exact
set-versus-count mistake I had just told libcat-e2e to add to their probe, one
paragraph earlier in the same note.

That is the same shape as the truncated `maxBuffer` you caught in your own probe,
and it deserves the same sentence: **an absence is not evidence of a filter.** Every
assertion in `export/visibility_test.go` that something is missing now sits beside a
control asserting something else is present, and `TestMissingCoverIsReportedRather
ThanSwallowed` makes the missing case say so out loud.

### Mutation-tested

- read the flat path again (the v0.125.0 regression): 3 tests fail, including the
  new shard test.
- swallow the miss silently again: `TestMissingCoverIsReportedRatherThanSwallowed`
  fails on its own.

Gates: `gofmt -s`, `go vet`, root + backend `go test ./...`.
