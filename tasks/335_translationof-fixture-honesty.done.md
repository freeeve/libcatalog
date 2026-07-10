# 335 -- libcodex v0.27.0 breaking: 76x-78x bf:relationship IRIs are now LC's real lowercase terms

Filed from libcodex on 2026-07-10 (cross-repo ask). Heads-up, not an action
item -- your production code is unaffected. One test uses an IRI worth fixing.

## What changed in libcodex v0.27.0

Every `bf:relationship` IRI libcodex emitted for the 76x-78x linking entries
(773, 776, 780, 785) was an invented camelCase code that **404s at id.loc.gov**
(`continues`, `otherPhysicalFormat`, `partOf`, `formedByUnionOf`, ...). v0.27.0
replaces them with marc2bibframe2's own lowercase terms, which resolve:

| tag / ind2 | was (404)             | now (200)             |
|------------|-----------------------|-----------------------|
| 773        | `partOf`              | `partof`              |
| 776        | `otherPhysicalFormat` | `otherphysicalformat` |
| 780 ind2 0 | `continues`           | `continuationof`      |
| 780 ind2 4 | `formedByUnionOf`     | `mergerof`            |
| 785 ind2 0 | `continuedBy`         | `continuedby`         |

LC's terms collapse several second indicators onto one term (780 ind2 5 and 6
both -> `absorptionof`), so the emitted `bf:Relation` now also carries the source
MARC field verbatim in an internal `bf:Note` (marcKey form,
`mnotetype/internal`); decode reads that note to reconstruct the exact tag,
indicators and subfields. Round trip is strictly more faithful than before.

## Why you're unaffected

Your production matching only ever looks at
`http://id.loc.gov/vocabulary/relationship/series`
(`ingest/enrich.go:380`, `project/project.go:57`). That IRI is emitted by the
490 series path and is **unchanged** by v0.27.0. Nothing in `ingest/` or
`project/` matches a 76x-78x term, so the breaking change does not touch your
series discrimination logic.

## The one thing to fix (test only)

`project/series_test.go:128` hand-builds a graph with
`<http://id.loc.gov/vocabulary/relationship/translationOf>` to stand in for a
765 linking entry (proving your `series` guard rejects a non-series relation).
That IRI is camelCase -- it is not a real LC term and it is **not** what libcodex
will emit once 765 support lands (libcodex task 113, piece 2): that will be
lowercase `translationof`. The test still passes (any non-`series` IRI exercises
the guard), but if you want the fixture to look like real libcodex output,
change it to `.../relationship/translationof`.

No rush and no production impact -- purely so the test data stays honest.

## Refs

- libcodex task 116 (the IRI fix, shipped in v0.27.0), 113 (the additive 76x
  extension that will emit `translationof` for 765), 112 (your adoption report
  that surfaced the dropped 765/830 relations).

## Outcome -- test-only, no release

`project/series_test.go:128` now uses
`http://id.loc.gov/vocabulary/relationship/translationof` (LC's real lowercase
term) instead of the invented camelCase `translationOf` for the non-series
relation that the series guard must reject. The fixture now matches what
libcodex v0.27.0 emits.

- **No production impact.** `Project`'s series discrimination only matches
  `.../relationship/series` (unchanged by v0.27.0); nothing in `ingest/` or
  `project/` matches a 76x-78x term. The test passed before and after -- any
  non-`series` IRI exercises the guard -- so this is purely fixture honesty.
- **No dependency bump.** The change is a string in libcat's own test data, not
  an adoption of libcodex v0.27.0; the go.mod require is untouched.
- **No release / no playground restart:** a test-data string changes no shipped
  behavior and no binary, so there is nothing to tag or adopt.
- `go build ./...` clean, `go test ./project/` green, gofmt -s clean.
