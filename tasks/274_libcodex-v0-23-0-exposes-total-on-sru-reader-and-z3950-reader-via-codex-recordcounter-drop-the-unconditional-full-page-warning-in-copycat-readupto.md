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
