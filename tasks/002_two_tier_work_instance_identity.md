# 002 — Two-tier Work / Instance identity + clustering

Framework core. `docs/ARCHITECTURE.md` §4, `docs/ROADMAP.md` Phase 1.

## Goal

Explicit two-tier identity: **Instance** (edition/format, the borrowable unit) ↔
**Work** (groups editions/translations/formats, the discovery unit). Opaque,
provider-independent ids at both levels.

## Approach

- `identity/` package: mint opaque Work + Instance ids; relate via
  `bf:instanceOf` / `bf:hasInstance`.
- Clustering instances → works:
  1. External ids where available — OpenLibrary (`OL...W`, free, ISBN-keyed)
     primary; OCLC Work ids where licensed; LC name-title authority for the
     authorized heading.
  2. Computed fallback — normalized author + title (+ original language) = the
     MARC 1XX+240 access-point key.
- A consumer's existing opaque id is typically edition-grained (qllpoc's
  "work-id" is minted per OverDrive title) → treat it as the **Instance** id and
  mint Works above it.
- **Clustering is correctable, not final.** Coverage is uneven and the computed
  key both over-merges (reissues, common titles) and under-merges (translations,
  name variants); fix mis-clusters via an `editorial:` merge/split overlay the
  computed key can't override. A merge/split must leave a redirect/tombstone so
  shared links and SEO survive.

## Acceptance

- Every instance resolves to exactly one Work; a report flags
  singletons/over-merges.
- Multi-format (ebook+audiobook) and translations collapse to one Work; distinct
  works do not merge. Clustering is reproducible.

## Refs

- OpenLibrary ISBN → edition → work API.
- Extraction reference: qllpoc `internal/hugogen/identity.go`, `cmd/roarindex`
  `clusterKey`.
