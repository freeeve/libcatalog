# 037 -- Record editing API

## Context

The cataloging editor's server half: read a grain as a typed document, accept
operation lists with optimistic locking, validate, hold drafts, and expose
merge/split and batch entry points. Depends on the WorkDoc mapper (tasks/041)
and override semantics (tasks/042) for full fidelity; the routes, ETag flow,
drafts, and batch plumbing land here.

## Scope

1. `GET /v1/works/{id}` (librarian): WorkDoc JSON + grain ETag header
   (ETag = RDFC canonical hash). Same for `GET /v1/instances/{id}`.
2. `PUT /v1/works/{id}` with `If-Match`: body = op list (`{op: set|add|remove|
   clear, path, value}`); applies through the doc mapper -> editorial quad delta
   -> publisher path; 412/409 on stale etag returns the current doc for
   three-way merge client-side.
3. `POST /v1/works/{id}/validate` (dry-run): returns the exact quad delta
   (diff preview) + profile validation findings; configurable editorial
   predicate whitelist enforced.
4. Drafts: `POST /v1/drafts`, `GET/PUT/DELETE /v1/drafts/{id}` (op lists in the
   datastore, per-user, autosave-friendly).
5. `POST /v1/works/merge` / `/v1/works/split` wrapping `AddMergeMarker`/
   `AddSplitMarkers` through the publisher (serialized -- single worker).
6. `POST /v1/batch`: `{selection, ops[]}` executed per-grain with per-op
   results; the shared selection model (ids now; search/savedQuery in 047).

## Acceptance

- 412 on stale ETag; successful PUT visible in a subsequent GET.
- Merge via API produces a grain byte-identical to `lcat merge`.
- An edit published to a DirStore tree flows through `lcat serialize && lcat
  project` into catalog.json (end-to-end demoable milestone with 041/042).
