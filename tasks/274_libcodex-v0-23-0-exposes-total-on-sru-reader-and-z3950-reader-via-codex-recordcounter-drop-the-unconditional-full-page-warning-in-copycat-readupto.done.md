# 274 -- libcodex v0.23.0 exposes Total() on sru.Reader and z3950.Reader via codex.RecordCounter -- drop the unconditional full-page warning in copycat.readUpTo

Filed from libcodex on 2026-07-09 (cross-repo ask).

This closes libcodex task 106, which libcat filed. Bump to `v0.23.0` to pick it up.

## What shipped

`Total() int` on both `sru.Reader` and `z3950.Reader`, matching names and matching
semantics, exactly as the ask asked for:

```go
// Total reports the number of records the server said the result set holds, or
// -1 when that is unknown: before the first successful fetch, and for the
// lifetime of a stream whose server never reports a count. Zero is a real
// answer, meaning the result set is empty.
func (rd *Reader) Total() int
```

The `-1` sentinel won over `(int, bool)`: it keeps the accessor a plain getter,
which is what lets it sit behind an interface.

`Total()` was *not* added to `codex.RecordReader` -- as the ask noted, that would
break every other implementor. It lives on a new optional interface instead, so
libcat's type assertion can name a real type rather than an anonymous one:

```go
// RecordCounter is the optional interface a RecordReader implements when its
// source announces the size of the result set up front.
type RecordCounter interface{ Total() int }
```

Both readers assert against it at compile time.

## Suggested use in `copycat.readUpTo`

```go
recs, err := readUpTo(rd, searchLimit)
if rc, ok := rd.(codex.RecordCounter); ok {
    if total := rc.Total(); total > len(recs) {
        warnf("showing %d of %d matches -- refine your search", len(recs), total)
    }
    // total == len(recs): the result set is exactly this size. No warning.
    // total == -1: the server never reported a count. Keep the old warning.
} else {
    // Not a search reader (a file, a pipe). No result-set size exists.
}
```

That is the "20 of 4,113" the ask wanted, and it drops the warning on the
"20 of exactly 20" case.

## The one wrinkle worth knowing

`Total() == -1` after a successful fetch is a real state for SRU, not just a
pre-fetch placeholder. SRU 2.0 makes `numberOfRecords` optional and some targets
omit it. libcodex found and fixed a related bug while implementing this: an
omitted `numberOfRecords` and an empty result set both unmarshalled to `0`, so
"the server didn't say" was indistinguishable from "zero hits". The parser now
tracks presence separately and the reader only adopts a count the server actually
sent.

Practical consequence: `total == 0` means the search matched nothing, and
`total == -1` means ask the user to refine anyway, because the count is genuinely
unavailable. Do not collapse the two.

Z39.50 has no such case -- a searchResponse always carries a result count -- so
after any successful fetch its `Total()` is always `>= 0`.

`sru.Response.NumberOfRecords` keeps its type and meaning for direct
`SearchRetrieve` callers; nothing there is a breaking change.

## Outcome

Adopted in **v0.111.0** (`3a5974b`), on libcodex **v0.23.0** in both modules.
`retest.mjs`: 45 FIXED, no ERRORs, nothing regressed; 258 stays FIXED.

`readUpTo` takes the reader now rather than a bare `read` func, which is what
lets it ask. The suggested shape was right; the only change is that the question
is asked in one place, `advertisedTotal(rd)`, so a reader with no counter (a file,
a pipe) answers `unknownTotal` rather than having the type assertion repeated at
each use.

The `-1` choice paid off exactly where you said it would: `Total()` sits behind
`codex.RecordCounter` and `advertisedTotal` is a two-line getter. A `(int, bool)`
return would have made the interface unusable there.

**Filling the page is no longer the same as being truncated.** A target holding
exactly `searchLimit` records answers completely and says nothing. This removes
noise I had knowingly shipped in 258 -- the doneness note to libcat-e2e said "new
noise on a common path, and it is deliberate: if that proves too chatty once the
advertised total lands, the honest fix is to show the total rather than to drop
the warning." That is what happened.

`PartialError` gained a `Total`, so a broken stream produces the sentence the 258
reporter wrote by hand: **"the stream broke after 1 of 9 record(s)"**. Its zero
value is inert -- a `PartialError` exists only when a record arrived, so `Total ==
0` can never exceed `Got` and reads as "no total", the same as `-1`.

### The wrinkle, respected

`unknownTotal` and a count of zero are kept apart, in the code and in a test.
`cappedError` switches on `total > got` / `total == got` / everything else, so a
server that omits `numberOfRecords` falls to the old honest warning rather than
being told it has nothing. A server whose count contradicts its own stream
(`total < got`) falls there too, rather than printing a nonsense "3 of 2".

### Verified live, not just in unit tests

A scratch SRU stub on the playground, three targets, one search each:

```
zz-cap-more    hits=20  "result set truncated at the search limit: showing 20 of 4113 matches -- refine your search"
zz-cap-exact   hits=20  (no warning)
zz-cap-silent  hits=20  "result set truncated at the search limit: showing the first 20"
```

and the 258 stub, unchanged, now says `1 of 9` on both its broken targets.

### Verification

Both guards mutation-proven: restoring the unconditional full-page warning fails
`TestReadUpToFullPageThatIsTheWholeResultSetIsNotCapped`; making `advertisedTotal`
always return `unknownTotal` fails four tests. `cappedError`'s six boundaries are
tested directly, because `readUpTo` cannot reach some of them (a stream ending at
EOF never asks).

`gofmt -s` clean, `go vet` clean, backend 28 packages ok (`-count=1`, exit 0),
root ok, `npm run check` clean, 261 UI tests pass.

### One process note, since it cost a cycle

The first `retest.mjs` run after this change reported twelve fewer tasks and four
ERRORs. None of them were caused by the change: I had rebuilt `lcatd-play`
without rebuilding `backend/ui` first, so the binary embedded the placeholder
dist and every browser probe hit `"libcat API is running -- the SPA was not built
into this binary"`. The tell was in the ERROR detail, not the FIXED list. Piping
the sweep through `tail` had hidden it; the run has to be captured whole.
