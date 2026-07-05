# 101 -- identity: records with no author/title/language all cluster into one Work

> Filed from the 2026-07-05 full-code review.

## Symptom

Two unrelated records that each lack a primary author, main title, and language
(e.g. a crosswalk that failed to populate 1XX/245) are merged into a single Work:
the second record's Instance attaches to the first record's Work.

## Cause

`identity.WorkKey(author, title, lang)` (identity/identity.go:61) always emits
`NormalizeKey(a) + "\x1f" + NormalizeKey(t) + "\x1f" + lang`, so for all-empty
inputs it returns `"\x1f\x1f"` -- never `""`. The guard in `SeedWorkKey`
(`if clusterKey != ""`, identity/resolver.go:98) that was clearly intended to skip
empty keys is dead code, and `Resolve` stores the key unconditionally
(resolver.go:139). The first empty-key record mints Work wA and sets
`workByKey["\x1f\x1f"] = wA`; every subsequent empty-key record clusters onto wA.
`SeedResolver` (identity/scan.go:124) re-seeds committed empty-key Works through
the same ineffective guard, so the over-merge persists across re-ingest.

## Fix sketch

Treat an all-empty access-point key as "no key": have `WorkKey` return `""` when
all normalized fields are empty (or check in `Resolve`/`SeedWorkKey`), and in
`Resolve` mint a fresh Work without recording the key in `workByKey`. Consider
whether a title-only or author-only key (one empty field) should also be treated
as too weak to cluster on -- decide and document.

## Acceptance

- Two records with empty author/title/lang resolve to two distinct Works, stably
  across re-ingest.
- A regression test covers the empty-key path in both `Resolve` and
  `SeedWorkKey`.

## Resolved

`WorkKey` now returns `""` whenever the normalized main title is empty: a record
with no title has no usable access point, and clustering by author alone (or by
nothing) merges unrelated books -- so title-less records always mint. `Resolve`
skips both the lookup and the `workByKey` store for an empty key; `SeedWorkKey`'s
existing guard now actually fires. This also heals the downstream consumers that
trusted the key (`findDuplicate` and `GET /v1/duplicates` treated the shared
`"\x1f\x1f"` key as a real cluster and would have flagged all title-less works
as duplicates of each other); both already skip empty keys. Decision recorded:
title empty = no key; author-less but titled records still cluster by
title+lang (anonymous works). New `TestEmptyKeyNeverClusters` covers fresh
resolve and the seeded path.
