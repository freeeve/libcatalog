# 245 -- lcat covers -- find and reap cover blobs no grain references

Opened 2026-07-09.

## Context

tasks/243 stopped `PUT /v1/works/{id}/cover` from leaving the previous format's
blob behind. It did not clean up behind itself: every cover replaced with a
different format between tasks/215 and v0.95.0 left an image in the blob store,
still served from `GET /covers/<workId>.<ext>` -- public, unauthenticated, and
guessable.

Nothing in the catalog references those blobs, so nothing will ever collect
them, and nothing can even tell an operator they exist. From the doneness note
filed to libcat-e2e:

> We plugged the hole; we did not clean up behind it.

A cataloger replaces a cover precisely when the old image must stop being
published -- wrong edition, rights complaint, an image that should not have gone
out. A takedown that looks done is not done, and after v0.95.0 it *looks* done
for every future replacement while the historical ones stay up.

## Scope

`lcat covers --store <blob-root>`:

- Walks `data/covers/`, resolves each blob back to its Work's grain, and reports
  every blob the grain does not reference.
- Reasons are distinguished, because they mean different things to an operator:
  a blob for a work that no longer exists, a blob for a work whose cover is a
  different format, a blob for a work with no cover at all, and a blob whose
  path does not parse as a cover.
- Read-only by default. `--reap` deletes, and says what it deleted.
- `--json` for scripting.

A work's cover is `bibframe.CoverOf` -- an editorial statement overlays a
feed-carried one -- so a work whose cover statement points at an external
provider URL has no local blob it references, and all of its blobs are orphans.

## Non-goals

- Reaping anything but covers.
- Touching grains. This command never writes a grain; the cover statement is the
  authority and it is already correct.

## Acceptance

- On a store with a replaced-format cover, the command names the stale blob and
  `--reap` removes it, leaving the referenced one.
- A work with no cover statement but a stored blob is reported.
- A blob whose work's grain is missing is reported.
- The referenced cover is never reported and never deleted, including when the
  work carries a feed cover that an editorial one overlays.
- Re-running after `--reap` reports nothing.
