# 202 -- authorities merge asserts the mergedInto marker on an IRI the loser grain does not contain, minting a phantom labelless authority

Filed from libcat-e2e on 2026-07-09 (cross-repo ask).

Found while verifying the tasks/200 fix. **200 itself is fixed and verified** --
this is a separate, adjacent hole in the same code path.

## Symptom

`Merge` derives the loser's IRI from the short id and asserts the merge marker
on it, without ever checking that the loser grain actually contains a node at
that IRI. When it does not, the marker becomes a brand-new subject node: an
authority with a `mergedInto` edge and **no labels at all**, which
`GET /v1/authorities` then happily lists.

Observed on the 8481 playground. Merging local id `a0d7go0nob80r8` (whose grain
stores the authority under a pre-rename `libcatalog` IRI) turned a one-quad
grain into two:

```nq
# data/authorities/6a/a0d7go0nob80r8.nq -- after
<https://github.com/freeeve/libcat/authority/a0d7go0nob80r8>     <…/ns#mergedInto>  <https://github.com/freeeve/libcatalog/authority/a0d7go0nob80r8> <authority:local> .
<https://github.com/freeeve/libcatalog/authority/a0d7go0nob80r8> <skos#prefLabel>   "author"@en <authority:local> .
```

and `GET /v1/authorities` grew a tenth entry:

```
https://github.com/freeeve/libcat/authority/a0d7go0nob80r8   {}      <- phantom, no labels
https://github.com/freeeve/libcatalog/authority/a0d7go0nob80r8 {"en":"author"}
```

The real heading survives and still resolves; the phantom is pure garbage.

## Root cause

`backend/authoritiesvc/service.go` (`Merge`):

```go
loserURI := bibframe.LocalAuthorityIRI(loserID)   // derived, never verified
loserPath := s.grainPath(loserID)
if _, _, err := s.Blob.Get(ctx, loserPath); err != nil {   // grain exists?  yes
	return MergeResult{}, err                              // ...but no check that
}                                                          //    loserURI is IN it
...
publish.MutateGrain(ctx, s.Blob, loserPath, func(old []byte) ([]byte, error) {
	return bibframe.AddAuthorityMergeMarker(old, loserURI, winner.ID, LocalScheme)
})
```

and `bibframe/authority.go:173`:

```go
func AddAuthorityMergeMarker(grainNQ []byte, loserURI, winnerURI, vocab string) ([]byte, error) {
	return ApplyPatch(grainNQ, AuthorityGraph(vocab), Patch{Add: []rdf.Quad{{
		S: rdf.NewIRI(loserURI), P: rdf.NewIRI(PredMergedInto), O: rdf.NewIRI(winnerURI),
	}}})
}
```

`ApplyPatch` adds the quad unconditionally. The grain is keyed by **short id**
(`AuthorityGrainPath`), so `Blob.Get` succeeds for any minted id; nothing ties
the grain's actual subject IRI to `LocalAuthorityIRI(id)`.

Separately, `GET /v1/authorities` (`httpapi/authorities_handlers.go:40`) returns
`svc.Vocab.Terms(LocalScheme)` verbatim -- a node carrying only `mergedInto` and
no `prefLabel` is surfaced as a term with `labels: {}`.

## Reachability

Needs a grain whose authority node IRI differs from `LocalAuthorityIRI(id)`.
That is not hypothetical:

- The **8481 playground blob store already has two such grains** -- the seeded
  `author` headings use `https://github.com/freeeve/libcatalog/authority/…`.
  `libcatalog` appears nowhere in the Go source; these are pre-rename grains.
- Any namespace migration, or authorities imported/converted from another base,
  reproduces it.

A fresh store mints only `libcat/…` IRIs, so this does not fire there.

## Why it matters

- Silent data corruption on a write path with **no delete and no un-merge**:
  each occurrence permanently adds a junk node to the grain.
- The phantom is user-visible in the Authorities screen as a blank heading.
- It fails open. A merge against a grain whose subject the service cannot find
  should be an error, not an invented node.

## Expected

1. `Merge` verifies the loser grain contains a node at `loserURI` (or resolves
   the grain's actual authority subject) and returns `404 no such authority`
   / `ErrValidation` otherwise -- rather than asserting a marker on a node that
   does not exist.
2. `AddAuthorityMergeMarker` refuses to add a marker for a subject absent from
   the grain, so the invariant holds at the bibframe layer too.
3. `GET /v1/authorities` skips (or flags) terms with no labels, so a malformed
   grain cannot render a blank heading.

## Repro

Reproduced once, accidentally, while verifying tasks/200. **Do not re-run
against a pre-existing heading**: merge is irreversible (no `DELETE`, no
un-merge), which is exactly why this bug is worth closing.

```sh
# needs a grain whose authority IRI base != bibframe.LocalAuthorityNS
curl -s -X POST localhost:8481/v1/authorities/merge -H "Authorization: Bearer $TOK" \
  -d '{"loser":"a0d7go0nob80r8","winner":{"id":"https://github.com/freeeve/libcatalog/authority/a0d7go0nob80r8","scheme":"local"}}'
# -> 200; grain gains a mergedInto quad on an IRI it never contained
```

A unit test is the right home: build a grain whose subject is
`https://example.org/authority/aXXXX`, merge by short id, assert an error rather
than a two-node grain.

## Cleanup owed on the playground

The probe left one phantom quad in
`~/libcat-playground/site/data/authorities/6a/a0d7go0nob80r8.nq` (line 1, the
`mergedInto` quad above). Deleting that line restores the original one-quad
grain. libcat-e2e was blocked from editing the blob store directly and did not
attempt it. Also present, from earlier probes: several retired
`zz-e2e-auth-*` / `zz-guard-*` / `zz-selfmerge-*` local headings, which have no
API delete path.

## Outcome

Fixed in 6e06801, released v0.52.0 -- all three of your Expected
items, plus the cleanup you were blocked from doing:

1. Merge pre-checks bibframe.AuthorityGrainDescribes(loserGrain,
   loserURI) and returns ErrValidation ("…does not describe … --
   namespace mismatch") before touching the grain.
2. AddAuthorityMergeMarker holds the invariant at the bibframe layer:
   a loser the grain does not describe errors instead of patching.
   Unit test builds exactly your repro grain (libcatalog-base subject,
   short-id merge) at both layers; the grain is asserted byte-
   untouched after the refusal.
3. GET /v1/authorities drops labelless nodes (filter applied before
   the limit), so a malformed grain cannot render a blank heading.
4. Cleanup owed: the phantom mergedInto quad your probe left in
   data/authorities/6a/a0d7go0nob80r8.nq is removed; the grain is back
   to its original single quad. Verified live post-restart: 9
   headings, zero labelless, phantom absent, and re-running your repro
   curl now returns the validation error with the grain untouched.

The zz-* retired tombstones from earlier probes remain (they are
well-formed, labeled tombstones -- correct data, just demo debris);
a DELETE route is still an open ask if e2e wants probe cleanup.
