# HTTP API reference

The `lcatd` HTTP surface. Every path below is served by `backend/httpapi`; the
table at the end is **generated from the router itself** (see
[Keeping this honest](#keeping-this-honest)), so it cannot drift from the code.

This is the staff and integration surface. The *published* catalog is a static
site (`hugo/`) built from a projection and served without `lcatd` -- nothing a
reader does hits these endpoints. When this document says "public" it means
*unauthenticated*, not *for readers*: the public routes are the login exchange,
the health check, the term lookups the editor's chip renderer uses, and the
patron-facing suggestion/concern intake.

## Authentication

`POST /v1/auth/login` with `{"email":…,"password":…}` returns
`{"accessToken":…,"refreshToken":…}`. Send the access token as
`Authorization: Bearer <token>` on every gated route. `POST /v1/auth/refresh`
trades a refresh token for a new access token; `POST /v1/auth/logout` retires
it. With OIDC configured, `POST /v1/auth/exchange` swaps an identity-provider
code for the same pair.

A missing or malformed token is `401 {"error":"missing bearer token"}` or
`401 {"error":"invalid token"}`. A valid token whose identity lacks the
route's role is `403 {"error":"insufficient role"}`.

### Roles

Roles are hierarchical -- `auth.Require(role)` admits that role **or higher**:

    patron  <  moderator  <  librarian  <  admin

| Role | Holds | Typical routes |
| --- | --- | --- |
| `public` | no token needed | login, health, term lookup, suggestion intake |
| `moderator` | the review queue | `/v1/queue`, `/v1/review`, `/v1/promotions`, `/v1/tags` |
| `librarian` | the cataloging surface | records, MARC, copycat, batch, exports, authorities |
| `admin` | deployment configuration | users, profiles, vocab sources, enrichment |

The `Role` column in the generated table names the **minimum** role, taken from
each middleware's `auth.Require(verifier, auth.RoleX)` initializer. Read it as
"this role or higher". Do not infer the role from a middleware variable's name
in the source: `staff` requires `moderator` and `adminOnly` requires plain
`admin`.

## Conventions

- **Errors** are `{"error":"<message>"}` with the status. Validation refusals
  that anchor to a field carry `{"error":…,"fields":[{"path":…,"message":…}]}`
  (tasks/077).
- **Unknown `/v1/` paths** return a JSON `404` rather than the SPA's HTML, so a
  mistyped endpoint fails as an API call.
- **Optimistic concurrency**: `PUT /v1/works/{id}` takes `If-Match: <etag>` and
  answers `412` with the current state when the record moved underneath the
  client. Server-initiated writes (merge, split, batch, relations, covers) own
  their own retry and take no client token.
- **Work ids** match `^w[a-z0-9]{13}$`; a malformed id is `400 "bad work id"`,
  a missing record `404`.
- **Content negotiation** is by path, not header: `/marc`, `/doc`, and the
  export formats each have their own route.

## Notable endpoints

### Reporting: `/v1/stats` and `/v1/audit`

Both are librarian-gated reads over the **audit log** -- staff editing activity
for one month, not anything about the collection's contents.

    GET /v1/stats                -> {"month":"2026-07","total":9,"actors":1,"works":1,"byAction":{…}}
    GET /v1/stats?month=2026-05  -> the same shape for May
    GET /v1/audit                -> {"month":"2026-07","entries":[…]}

`month=YYYY-MM` is **optional and defaults to the current UTC month**
(tasks/234). The distinction is between absent and wrong: omitting `month`
selects this month, while a malformed value refuses with
`400 "month must be YYYY-MM, e.g. month=2026-07"`.

### Exports

`POST /v1/exports` queues a job (`csv`, `marc`, `nquads`, `jsonld`); whole-corpus
selections run on a worker, narrow ones run in-request. `GET /v1/exports/{id}`
polls it. `GET /v1/exports/{id}/download` is **public by path but not by
obscurity**: it takes a signed, expiring token in the query string, so a
download link can be handed to a browser without a bearer header.

CSV is the human-facing format and resolves controlled subjects to labels
through the loaded term index (tasks/233); the machine-readable formats carry
authority IRIs. See [marc-fidelity](marc-fidelity.md) for what survives a MARC
round trip.

### Public intake

`POST /v1/suggestions` and `POST /v1/concerns` accept unauthenticated patron
input, rate-limited behind a proof-of-work challenge issued by
`GET /v1/challenge`. `GET /v1/terms`, `GET /v1/term`, and
`GET /v1/terms/resolve` answer authority lookups without a token -- the editor's
chip renderer and the published site's term pages both read them.

### Everything else

The remaining surfaces are grouped by path prefix and named plainly:
`/v1/works` (records, MARC, items, covers, attachments, relations, clone,
merge/split, visibility), `/v1/copycat` (SRU targets, search, import batches),
`/v1/batch` + `/v1/macros` + `/v1/queries` (bulk edit machinery),
`/v1/authorities` + `/v1/vocabsources` (controlled vocabulary),
`/v1/profiles` (editing profiles), and `/v1/users` (accounts and roles).

## Keeping this honest

`TestAPIReferenceMatchesRouter` (`backend/httpapi/apidoc_test.go`) parses every
`mux.Handle` / `mux.HandleFunc` registration in the package and compares the
result to the table below. **Adding a route without documenting it fails the
test.** Roles are resolved from the `auth.Require` initializer that produced
each middleware, propagated through helpers that take one as a parameter, so
the column reports what the router enforces rather than what a variable is
called.

Regenerate after adding or re-roling an endpoint:

```sh
cd backend && go test ./httpapi -run TestAPIReference -update-apidoc
```

The gate covers method, path, and role -- the parts a machine can check.
Parameters and response shapes are prose above, and are not gated; when you add
an endpoint worth explaining, explain it there.

`ANY` in the Method column means the pattern carries no method and matches all
of them: `/` serves the admin SPA, `/v1/` is the JSON-404 catch-all.

<!-- BEGIN ROUTES (generated: go test ./httpapi -run TestAPIReference -update-apidoc) -->

| Method | Path | Role | Source |
| --- | --- | --- | --- |
| `ANY` | `/` | public | `httpapi.go` |
| `GET` | `/config` | public | `httpapi.go` |
| `GET` | `/covers/{file}` | public | `cover_handlers.go` |
| `ANY` | `/v1/` | public | `httpapi.go` |
| `GET` | `/v1/audit` | librarian | `review_handlers.go` |
| `POST` | `/v1/auth/exchange` | public | `httpapi.go` |
| `POST` | `/v1/auth/login` | public | `auth_handlers.go` |
| `POST` | `/v1/auth/logout` | public | `auth_handlers.go` |
| `POST` | `/v1/auth/refresh` | public | `auth_handlers.go` |
| `GET` | `/v1/authorities` | librarian | `authorities_handlers.go` |
| `POST` | `/v1/authorities` | librarian | `authorities_handlers.go` |
| `POST` | `/v1/authorities/merge` | librarian | `authorities_handlers.go` |
| `GET` | `/v1/authorities/profile` | librarian | `authorities_handlers.go` |
| `POST` | `/v1/authorities/reload` | librarian | `authorities_handlers.go` |
| `GET` | `/v1/authorities/{id}` | librarian | `authorities_handlers.go` |
| `PUT` | `/v1/authorities/{id}` | librarian | `authorities_handlers.go` |
| `POST` | `/v1/batch` | librarian | `records_handlers.go` |
| `POST` | `/v1/batch/ops` | librarian | `batch_handlers.go` |
| `POST` | `/v1/batch/resolve` | librarian | `batch_handlers.go` |
| `GET` | `/v1/challenge` | public | `suggest_handlers.go` |
| `POST` | `/v1/concerns` | public | `suggest_handlers.go` |
| `GET` | `/v1/copycat/batches` | librarian | `copycat_handlers.go` |
| `POST` | `/v1/copycat/batches` | librarian | `copycat_handlers.go` |
| `DELETE` | `/v1/copycat/batches/{id}` | librarian | `copycat_handlers.go` |
| `GET` | `/v1/copycat/batches/{id}` | librarian | `copycat_handlers.go` |
| `POST` | `/v1/copycat/batches/{id}/commit` | librarian | `copycat_handlers.go` |
| `POST` | `/v1/copycat/batches/{id}/revert` | librarian | `copycat_handlers.go` |
| `POST` | `/v1/copycat/batches/{id}/review` | librarian | `copycat_handlers.go` |
| `POST` | `/v1/copycat/original` | librarian | `copycat_handlers.go` |
| `GET` | `/v1/copycat/profiles` | librarian | `copycat_handlers.go` |
| `POST` | `/v1/copycat/profiles` | librarian | `copycat_handlers.go` |
| `DELETE` | `/v1/copycat/profiles/{name}` | librarian | `copycat_handlers.go` |
| `POST` | `/v1/copycat/search` | librarian | `copycat_handlers.go` |
| `GET` | `/v1/copycat/targets` | librarian | `copycat_handlers.go` |
| `POST` | `/v1/copycat/targets` | admin | `copycat_handlers.go` |
| `DELETE` | `/v1/copycat/targets/{name}` | admin | `copycat_handlers.go` |
| `GET` | `/v1/copycat/templates` | librarian | `copycat_handlers.go` |
| `POST` | `/v1/covers/batch` | librarian | `cover_batch.go` |
| `GET` | `/v1/drafts` | librarian | `drafts_handlers.go` |
| `POST` | `/v1/drafts` | librarian | `drafts_handlers.go` |
| `DELETE` | `/v1/drafts/{id}` | librarian | `drafts_handlers.go` |
| `GET` | `/v1/drafts/{id}` | librarian | `drafts_handlers.go` |
| `PUT` | `/v1/drafts/{id}` | librarian | `drafts_handlers.go` |
| `GET` | `/v1/duplicates` | librarian | `maintenance_handlers.go` |
| `GET` | `/v1/enrich` | admin | `enrich_handlers.go` |
| `POST` | `/v1/enrich/{source}/run` | admin | `enrich_handlers.go` |
| `GET` | `/v1/exports` | librarian | `export_handlers.go` |
| `POST` | `/v1/exports` | librarian | `export_handlers.go` |
| `GET` | `/v1/exports/{id}` | librarian | `export_handlers.go` |
| `GET` | `/v1/exports/{id}/download` | public | `export_handlers.go` |
| `GET` | `/v1/healthz` | public | `httpapi.go` |
| `GET` | `/v1/item-templates` | librarian | `batch_handlers.go` |
| `POST` | `/v1/item-templates` | librarian | `batch_handlers.go` |
| `DELETE` | `/v1/item-templates/{id}` | librarian | `batch_handlers.go` |
| `PUT` | `/v1/item-templates/{id}` | librarian | `batch_handlers.go` |
| `GET` | `/v1/macros` | librarian | `batch_handlers.go` |
| `POST` | `/v1/macros` | librarian | `batch_handlers.go` |
| `DELETE` | `/v1/macros/{id}` | librarian | `batch_handlers.go` |
| `PUT` | `/v1/macros/{id}` | librarian | `batch_handlers.go` |
| `GET` | `/v1/profiles` | librarian | `profiles_handlers.go` |
| `DELETE` | `/v1/profiles/{id}` | admin | `profiles_handlers.go` |
| `GET` | `/v1/profiles/{id}` | librarian | `profiles_handlers.go` |
| `PUT` | `/v1/profiles/{id}` | admin | `profiles_handlers.go` |
| `GET` | `/v1/promotions` | moderator | `promotion_handlers.go` |
| `POST` | `/v1/promotions` | moderator | `promotion_handlers.go` |
| `POST` | `/v1/promotions/decide` | librarian | `promotion_handlers.go` |
| `POST` | `/v1/publish` | librarian | `review_handlers.go` |
| `GET` | `/v1/queries` | librarian | `batch_handlers.go` |
| `POST` | `/v1/queries` | librarian | `batch_handlers.go` |
| `DELETE` | `/v1/queries/{id}` | librarian | `batch_handlers.go` |
| `GET` | `/v1/queue` | moderator | `review_handlers.go` |
| `POST` | `/v1/review` | moderator | `review_handlers.go` |
| `GET` | `/v1/stats` | librarian | `review_handlers.go` |
| `POST` | `/v1/suggestions` | public | `suggest_handlers.go` |
| `GET` | `/v1/tags` | moderator | `promotion_handlers.go` |
| `GET` | `/v1/term` | public | `terms_handler.go` |
| `GET` | `/v1/terms` | public | `terms_handler.go` |
| `POST` | `/v1/terms` | librarian | `review_handlers.go` |
| `GET` | `/v1/terms/resolve` | public | `terms_handler.go` |
| `GET` | `/v1/users` | admin | `auth_handlers.go` |
| `POST` | `/v1/users` | admin | `auth_handlers.go` |
| `DELETE` | `/v1/users/{email}` | admin | `auth_handlers.go` |
| `PUT` | `/v1/users/{email}/roles` | admin | `auth_handlers.go` |
| `POST` | `/v1/vocabcache` | librarian | `vocabsources_handlers.go` |
| `GET` | `/v1/vocabsources` | librarian | `vocabsources_handlers.go` |
| `POST` | `/v1/vocabsources` | admin | `vocabsources_handlers.go` |
| `DELETE` | `/v1/vocabsources/{name}` | admin | `vocabsources_handlers.go` |
| `POST` | `/v1/vocabsources/{name}/download` | admin | `vocabsources_handlers.go` |
| `DELETE` | `/v1/vocabsources/{name}/snapshot` | admin | `vocabsources_handlers.go` |
| `PUT` | `/v1/vocabsources/{name}/snapshot` | admin | `vocabsources_handlers.go` |
| `GET` | `/v1/vocabsuggest` | librarian | `vocabsources_handlers.go` |
| `GET` | `/v1/withdrawn` | librarian | `maintenance_handlers.go` |
| `GET` | `/v1/works` | librarian | `works_list_handler.go` |
| `POST` | `/v1/works/merge` | librarian | `records_handlers.go` |
| `POST` | `/v1/works/split` | librarian | `records_handlers.go` |
| `GET` | `/v1/works/{id}` | librarian | `records_handlers.go` |
| `PUT` | `/v1/works/{id}` | librarian | `records_handlers.go` |
| `GET` | `/v1/works/{id}/attachments` | librarian | `attachment_handlers.go` |
| `POST` | `/v1/works/{id}/attachments` | librarian | `attachment_handlers.go` |
| `DELETE` | `/v1/works/{id}/attachments/{name}` | librarian | `attachment_handlers.go` |
| `GET` | `/v1/works/{id}/attachments/{name}` | librarian | `attachment_handlers.go` |
| `POST` | `/v1/works/{id}/clone` | librarian | `clone_handlers.go` |
| `DELETE` | `/v1/works/{id}/cover` | librarian | `cover_handlers.go` |
| `PUT` | `/v1/works/{id}/cover` | librarian | `cover_handlers.go` |
| `GET` | `/v1/works/{id}/doc` | librarian | `records_handlers.go` |
| `GET` | `/v1/works/{id}/identifiers` | librarian | `subject_lookup.go` |
| `GET` | `/v1/works/{id}/items` | librarian | `maintenance_handlers.go` |
| `PUT` | `/v1/works/{id}/items` | librarian | `maintenance_handlers.go` |
| `POST` | `/v1/works/{id}/items/bulk` | librarian | `items_bulk.go` |
| `GET` | `/v1/works/{id}/marc` | librarian | `marc_handlers.go` |
| `POST` | `/v1/works/{id}/marc` | librarian | `marc_handlers.go` |
| `POST` | `/v1/works/{id}/marc/preview` | librarian | `marc_handlers.go` |
| `POST` | `/v1/works/{id}/ops` | librarian | `records_handlers.go` |
| `DELETE` | `/v1/works/{id}/relations` | librarian | `relations_handlers.go` |
| `GET` | `/v1/works/{id}/relations` | librarian | `relations_handlers.go` |
| `POST` | `/v1/works/{id}/relations` | librarian | `relations_handlers.go` |
| `POST` | `/v1/works/{id}/subjects/lookup` | librarian | `subject_lookup.go` |
| `GET` | `/v1/works/{id}/suggestions` | public | `suggest_handlers.go` |
| `POST` | `/v1/works/{id}/validate` | librarian | `records_handlers.go` |
| `GET` | `/v1/works/{id}/visibility` | librarian | `maintenance_handlers.go` |
| `POST` | `/v1/works/{id}/visibility` | librarian | `maintenance_handlers.go` |
| `POST` | `/v1/works/{id}/withdrawn` | librarian | `maintenance_handlers.go` |

<!-- END ROUTES -->
