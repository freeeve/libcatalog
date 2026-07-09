# 258 -- a copycat search stream that breaks after the first page silently returns partial results: the same error is reported as a failure on page 1 and hidden on page 2

Filed from libcat-e2e on 2026-07-09 (cross-repo ask).

Found while exercising `readUpTo`'s partial-results path. **The partial-results policy
itself is right and should stay** -- what is wrong is that nothing tells the client the
results are partial, and the visibility of a given error depends only on which SRU page
it happened to land on.

## Symptom

`POST /v1/copycat/search` against a target whose stream dies after page 1 returns
page 1's hits, `failures: {}`, and HTTP 200. The result set is indistinguishable from a
complete one.

Measured on :8481 against a local SRU stub (no third party queried). The stub serves
page 1 with **one** record and `<numberOfRecords>9</numberOfRecords>`, then answers the
page-2 request with a malformed body:

```
target                 page 1        page 2        -> results  failure
zz-e2e-str-ok          2 records     empty         ->    2     (none)     <- control
zz-e2e-str-bad         GARBAGE       --            ->    0     "sru: parse response: XML syntax error on line 1: unexpected EOF"
zz-e2e-str-trunc       1 record      GARBAGE       ->    1     (none)     <- the bug
zz-e2e-str-t500        1 record      HTTP 500      ->    1     (none)
zz-e2e-str-diag        SRU diagnostic              ->    0     "sru: Unsupported index: bath.isbn"

stub page requests: {"/ok":2,"/bad":1,"/diag":1,"/trunc":2}   <- page 2 really was fetched
```

`zz-e2e-str-bad` and `zz-e2e-str-trunc` serve the **identical** malformed body. The only
difference is which page it lands on:

```
page 1 -> failure "sru: parse response: XML syntax error on line 1: unexpected EOF"
page 2 -> failure  undefined
```

It is not an XML-parsing quirk: an HTTP 500 on page 2 disappears the same way
(`zz-e2e-str-t500`). And the `numberOfRecords=9` the server advertised is discarded, so
"1 result" is all the caller ever learns.

## Root cause

`backend/copycat/search.go:201-218`:

```go
func readUpTo(read func() (*codex.Record, error), limit int) ([]*codex.Record, error) {
	var out []*codex.Record
	for len(out) < limit {
		rec, err := read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			// Partial results beat none: a mid-stream error after hits is
			// swallowed; an immediate error surfaces.
			if len(out) > 0 {
				break
			}
			return nil, err
		}
		out = append(out, rec)
	}
	return out, nil
}
```

The comment describes the behaviour accurately. The problem is the return type: the
function can only say "records" *or* "error", so keeping the records means throwing the
error away entirely. `SearchAll` (`search.go:88-97`) then has nothing to record:

```go
recs, err := search(ctx, t, terms, searchLimit)
mu.Lock()
defer mu.Unlock()
if err != nil {
	failures[t.Name] = err.Error()
	return
}
```

`err == nil`, so the target is reported as a clean success.

Upstream, `libcodex sru/reader.go:41` pages on demand and makes the error sticky:
*"A transport, parse or diagnostic error is sticky: once returned, every later call
returns it too."* Whether an error is the *first* call or a *later* one is decided by
the page size and how many records the server put on page 1 -- an implementation detail
of the remote server, not anything about the error.

Nothing downstream can compensate: `SearchResult` (`search.go:19-28`) carries
`Target/Title/Author/Date/ISBN/Edition/LCCN/Record` and no count, and
`CopyCat.svelte` only ever renders `st.results.length` ("N matches") plus the
`st.failures` entries (`:498`). There is no total, and no truncation marker, anywhere
in the chain.

## Why it matters

Copy cataloging exists so a cataloger does not re-key a record that already exists. The
whole judgement is *"is my book in this result set?"* -- and a truncated set answers
that question wrongly, with no hint that it is doing so.

A cataloger searches LOC by title, gets 8 hits on page 1, page 2 fails on a network
blip or a mid-stream 500. They see 8 results, none of them their edition, and conclude
the record is not there. They hand-catalog an original for a book LOC already has. The
same failure one record earlier would have shown them a red *"loc-sru: sru: parse
response…"* and they would have retried.

Two aggravating details:

- **`searchLimit = 20`** (`copycat.go:77`). Any target with more than 20 matches is
  *also* silently truncated -- that one is deliberate, but combined with the missing
  total it means the UI can never distinguish "20 matches" from "20 of 4,113".
- The quiet path is the *common* one. A server that rejects the query outright fails on
  page 1 and is reported. A server that works and then hiccups -- the ordinary
  real-world failure of a long-running federated search -- is the one that goes silent.

Nothing is corrupted, and no bad record is staged: the records that do come back are
whole and valid. What is lost is the cataloger's ability to trust an empty-looking
answer.

## Expected

Keep the partial results. Report the truncation.

- **Let `readUpTo` return both**, e.g. `(recs []*codex.Record, truncated error)`, and
  have `protocolSearch` pass it up. The information exists at `search.go:209`; it is
  discarded one line later.
- **Add a per-target warning channel** beside `failures`, or reuse `failures` with a
  distinguishing shape -- the client already renders that map
  (`CopyCat.svelte:498`), so `{"loc-sru": "partial results: sru: … (1 of 9)"}` needs no
  new UI. A `warnings` map alongside `results` and `failures` is cleaner, since a
  partial success is not a failure and must not suppress the hits.
- **Carry the advertised total.** SRU returns `numberOfRecords` and the reader already
  parses it; surfacing it (per target, beside the results) is what lets the screen say
  *"20 of 4,113 — refine your search"* and *"1 of 9 — this target's response was
  truncated"*. That subsumes the `searchLimit` case too.

If the swallow is meant to stay exactly as is, the comment should say why a cataloger
being unable to tell a truncated set from a complete one is acceptable -- because on the
evidence above, it is the one thing they most need to know.

## Repro

```bash
cd ~/libcat-e2e && node harness/probe_copycat_stream.mjs   # S5, S7, S8
cd ~/libcat-e2e && node harness/retest.mjs                 # check t258
```

The probe stands up an SRU stub on `127.0.0.1`, registers `zz-e2e-str-*` targets
pointed at it, searches each, and deletes them. Its controls are the load-bearing part:
`S1` proves a clean stream returns every record, `S2` proves the very same malformed
body **is** reported when it lands on page 1, and `S6` proves the stub was actually
asked for page 2 (so `S5` is measuring a real mid-stream break, not a one-page
response).

By hand: point a target at any server that answers page 1 with records and a
`<zs:nextRecordPosition>`, then fails the second `startRecord=` request.

```bash
TOK=…
curl -XPOST -H "Authorization: Bearer $TOK" -H 'Content-Type: application/json' \
  -d '{"name":"zzstub","url":"http://127.0.0.1:9999/trunc","protocol":"sru"}' \
  localhost:8481/v1/copycat/targets

curl -s -XPOST -H "Authorization: Bearer $TOK" -H 'Content-Type: application/json' \
  -d '{"query":"zz","targets":["zzstub"]}' localhost:8481/v1/copycat/search
# {"results":[ …1 record… ],"failures":{}}   <- page 2 died; nothing says so

curl -XDELETE -H "Authorization: Bearer $TOK" localhost:8481/v1/copycat/targets/zzstub
```
