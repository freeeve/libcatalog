# 096 -- Profile admin UI (runtime editing of editing profiles)

Split out of `052_paradise-polish.md`.

## Context

Editing profiles (`backend/profiles`) are the JSON documents that replace MARC
frameworks for the BIBFRAME-native editor. They were embedded-only at runtime:
four call sites each captured `profiles.LoadDefaults()` once, and the `LoadDir`
override path was wired only into the `profiles-validate` CLI, never the server.
Changing a profile meant editing git-reviewed JSON and redeploying. This adds
runtime editing, with overrides persisted to the blob store (durable across
restarts, like vocab snapshots -- independent of the in-memory-KV work in 095).

## What shipped

- **`backend/profilesvc`** -- the live profile set: shipped defaults overlaid
  with blob-persisted overrides (`data/profiles/<id>.json`). Copy-on-write via
  `atomic.Pointer[snapshot]` (mirrors `vocab.Index`): `Reload` builds a fresh
  snapshot and swaps it, so reads are lock-free and coherent. `Put` validates
  through `profiles.Parse` (the "framework test") before persisting under ETag
  optimistic locking (empty If-Match = create-only); `DeleteOverride` reverts to
  the shipped default. `Mapper()`/`Set()`/`List()`/`Get()` feed the handlers.
- **`profiles.Parse`** factored out so the embedded loader, `LoadDir`, and the
  service share one validator.
- **Refactor:** the four captured `defaultMapper`/`defaultProfiles`/
  `defaultAuthorityProfile`/`defaultBatchMapper` sites now read the live service
  per request; `batch.Service` gained a `MapperFn` provider. `httpapi.New`
  synthesizes a defaults-only, read-only service when none is wired (tests).
- **Endpoints** (`httpapi/profiles_handlers.go`): `GET /v1/profiles` (list, live,
  with an `overridden` flag) and `GET /v1/profiles/{id}` are librarian-gated;
  `PUT`/`DELETE /v1/profiles/{id}` are admin-gated with ETag locking and a
  `PROFILE_EDIT`/`PROFILE_REVERT` audit entry.
- **UI:** an admin `Profiles.svelte` screen (route `/profiles`, admin-gated nav +
  Dashboard card): profile list with default/overridden badges and a JSON
  editor with validate-on-save and revert-to-default. `canAdmin` helper added.

## Verified

Backend (incl. `-race` on profilesvc), UI type-check, and both test suites pass.
End-to-end on the demo playground: an admin override of `work-monograph` went
live immediately, showed `overridden` in the list, **survived a full server
restart** (blob persistence), reverted cleanly to the default; invalid JSON ->
400, stale ETag -> 412, unauthenticated -> 401.

## Not done (deliberate trim)

- Creating a brand-new profile id from scratch in the UI (only overriding the
  shipped ids). A future follow-up if a deployment needs bespoke profiles.
- No structured form editor; raw JSON + server validation is the MVP.
