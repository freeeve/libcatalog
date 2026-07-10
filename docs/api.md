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

One CSV row per Work. Alongside the bibliographic columns it summarizes the
Work's holdings across all its instances (tasks/058): `itemCount`, and the
distinct `callNumbers`, `locations` and `barcodes` among its items. Call
numbers and locations are deduplicated -- two copies on one shelf are one
location -- while barcodes, unique by definition, all appear. An item's free-text
note is not a column: it would carry the `; ` separator the columns join on, and
it is not a dimension anyone sorts by. `itemCount` is `0` rather than blank for
a Work with no holdings, so "what have we not received?" is a sort.

### Attachments

`POST /v1/works/{id}/attachments?name=<filename>` stores a staff working file
(20MB cap) and records an editorial `lcat:attachment` statement. The name is
the filename **as the cataloger's file carries it**, in any script; it is a
display name, not a path. The blob segment is derived from it by an injective
encoding, so two different filenames can never address the same bytes
(tasks/236).

An upload whose name is already attached refuses with `409`. Replacing is
deliberate: `DELETE` first, or `POST …&replace=true`. A name with a slash, a
control character, or over 100 bytes refuses with `400` -- and is refused
rather than rewritten, because a silent rename is how two documents came to
share one file.

Attachments are librarian-gated end to end, never projected to the public
catalog, and served as `application/octet-stream` with `nosniff`, so an
uploaded HTML file is a download and never a page.

### Editing: op lists

`POST /v1/works/{id}/ops` and `POST /v1/batch/ops` take the same op list.
An op names a `resource`, a `path`, and an `action`:

| `resource` | means | actions |
|---|---|---|
| `work` (or omitted) | the Work node | `add`, `remove`, `set`, `clear` |
| an Instance id | that Instance node | `add`, `remove`, `set`, `clear` |
| `items` | **every** `bf:Item` in the grain | `set`, `clear` |

`add` and `remove` carry a singular `value`; `set` carries a `values` array;
`clear` carries neither. An `add` with a `values` array is refused rather than
guessed at.

Item ops (tasks/058) are how a batch reaches holdings: item ids are minted per
grain, so a selection cannot name them, but "every copy shelved in Stacks" is
exactly what a batch relocation means. An item field holds one value, so `add`
and `remove` are refused there. An optional `where` restricts the edit to items
whose current value at `path` is exactly that string, which is what lets

```json
{"resource":"items","path":"location","action":"set",
 "values":[{"v":"Annex"}],"where":"Stacks"}
```

move the Stacks copies and leave the Reference ones alone. An item that does not
assert the field reads as `""`, so `"where":""` addresses exactly the items
missing it. Items already holding the target value are skipped, not rewritten:
a re-run reports an empty diff because it did nothing.

The editable item fields are `callNumber`, `location`, and `note`. **`barcode`
is not among them**: a barcode names one physical copy, so assigning one across
a selection would mint duplicates.

Batch runs refuse an op naming a specific Instance id -- that id belongs to one
grain, so across a selection it would edit whichever record happened to own it.
`items` is the one non-work resource a batch may name, because it addresses a
set rather than a node.

### Editing: raw quad patches

`PUT /v1/works/{id}` and `POST /v1/batch` take a quad-level patch (`add` /
`remove` statements) rather than an op list. They are the machine surface; the
admin UI uses the op endpoints above. Predicates are checked against an
editorial allowlist.

**A patch's subject must name the work it edits.** For `PUT`, a subject that is
a Work node must be *this* work's; for `POST /v1/batch`, whose single patch is
applied to many works, the Work-node subject is **rebound to each work in turn**
(tasks/240). Without that, one patch wrote quads describing the first work into
every other selected work's grain, and the dry run agreed with the corruption
because it diffed the same verbatim patch.

`POST /v1/batch` therefore refuses a patch it cannot rebind: a subject that is
not a Work node (an Instance node or a skolem child names a node in one grain
and nothing at all in another), or an object that is a grain-local IRI. `PUT`
keeps accepting those, since a single-record patch legitimately mints its own
skolem and instance nodes and there is nothing to rebind them to.

The rule is enforced at the route, not in `bibframe.ApplyEditorialPatch`, because
a grain describing *another* Work node is not universally wrong: a merge marker
(`lcat:mergedInto`) lives in the survivor's grain with the **retiring** work as
its subject. That is deliberate provenance, and the identity resolver reads it.
The consequence is that `PUT /v1/works/{id}` can no longer hand-write a merge
marker -- use `POST /v1/works/merge`, which is the audited path for it.

### Public intake

`POST /v1/suggestions` and `POST /v1/concerns` accept unauthenticated patron
input, rate-limited behind a proof-of-work challenge issued by
`GET /v1/challenge`. `GET /v1/terms`, `GET /v1/term`, and
`GET /v1/terms/resolve` answer authority lookups without a token -- the editor's
chip renderer and the published site's term pages both read them.

### `GET /v1/works`: retired records are excluded by default

`?tombstoned=` selects which records the search runs over:

| value | meaning |
|---|---|
| `exclude` (default, or omitted) | live records only |
| `include` | live and retired together |
| `only` | retired records only -- the audit question, "what did I retire?" |

Anything else is a `400`, rather than a silent fall back to the default: a client
that asked for `only` and was shown `exclude` would read the empty list as "the
records are gone" (tasks/280).

The filter runs **before** the query, before the facet counts, and before paging,
so `total`, `matched`, `facets` and the `works` window all describe the same set.
This is why `total` is not "everything in the catalog": under the default it is
the number of live records. A client filtering the response instead would report
`matched: 4` and render one row.

### Everything else

### `POST /v1/review`: `reviewed` means applied

The review queue is a shared worklist, so two moderators routinely stage
decisions against the same suggestion. A decision that arrives after someone
else has already resolved that suggestion is **discarded** -- the optimistic
concurrency check the queue needs -- and the response says so:

```json
{ "reviewed": 1, "staleDecisions": [ { "workId": "...", "term": {...}, "type": "ADD", "approve": true } ] }
```

`reviewed` counts the decisions **applied**, never the decisions submitted. The
discarded ones come back in `staleDecisions` so a client can re-stage them
against their new status and tell the human, rather than reporting work that did
not happen (tasks/257).

`staleDecisions` is deliberately not called `skipped`: when `publish: true`, the
publisher's own `skipped` count is merged into this same object and means
something else entirely (suggestions it did not publish).

### Health probes: `/v1/healthz` and `/v1/readyz`

Two probes that answer different questions, and must be wired to different
things.

`GET /v1/healthz` is **liveness**: is this process wedged? It reports on the
process and nothing else, and it keeps returning `200` while the server drains.
Point a container liveness probe here. Never make it depend on a datastore -- a
failing liveness probe restarts the container, so a dependency blip wired to
liveness becomes a restart storm.

`GET /v1/readyz` is **readiness**: should this replica receive traffic? It
returns `200` normally and `503 {"status":"draining"}` once the process has
received `SIGTERM`. Point a readiness probe here.

Readiness deliberately does **not** check store connectivity, though it easily
could. Every replica shares one store, so a store blip would fail every
replica's readiness at once and the orchestrator would empty the Service of
endpoints -- converting a degradation that still serves cached reads into a
total outage. A probe whose failure mode is "remove all capacity" must not
depend on anything shared.

Readiness earns its keep at shutdown. Kubernetes removes a terminating pod from
its Service endpoints *concurrently* with sending `SIGTERM`, not before it, so
for the width of that race a load balancer still routes to a server that has
already stopped listening. Set `LCATD_SHUTDOWN_DELAY` (e.g. `5s`, comfortably
more than one readiness period) and `lcatd` will fail readiness immediately on
`SIGTERM`, keep serving for the delay while it is deregistered, and only then
drain in-flight requests. The default is `0` -- immediate drain -- which is what
a local or single-process run wants.

### `POST /v1/covers/batch`

A zip whose entries are `<workId>.<ext>` or `<isbn>.<ext>`. The response counts
three disjoint outcomes, and the counts are the report a librarian acts on:

```json
{
  "applied": 460,
  "skipped": 39,
  "failed": 1,
  "results": [
    {"file": "w01….png", "workId": "w01…", "cover": "covers/w01….png"},
    {"file": "nope.png", "skipped": "not a work id or known isbn"},
    {"file": "w02….png", "workId": "w02…", "failed": "cover store failed"}
  ]
}
```

`skipped` means the entry was rejected before either store was touched: nothing
happened, and the record is untouched. `failed` means the stores were asked to
do the work and did not; the entry is worth retrying once the store recovers. A
`failed` entry that also carries `"changed": true` wrote a cover statement that
could not be rolled back -- that record claims an image whose bytes are missing,
and it is the one entry a person has to repair. Those entries are in the audit
log; compensated ones changed nothing and are not (tasks/268, compare tasks/249).

Any `failed` entry makes the response **`207 Multi-Status`**. A `200` means every
entry either applied or was skipped without touching a record.

### `PUT /v1/works/{id}/items`

A client-token PUT, like `PUT /v1/works/{id}`. It replaces one instance's
holdings **wholesale** from the list in the body, so it must be checked against
the read it was computed from:

- No `If-Match` -> **`428`** `{"error":"If-Match required"}`.
- A stale `If-Match` -> **`412`** carrying the fresh grain and its ETag, so the
  client can show the edit that beat it and rebase.

Send the `etag` that `GET /v1/works/{id}/items` returned. Until tasks/273 the
route read no precondition at all: two catalogers with the item panel open on
one record would each save their own list, the second deleting the first's copy,
and both were told `200`. A barcode names one physical copy on one shelf, so the
lost item is a shelf silently unlinked from the catalog.

`POST /v1/works/{id}/items/bulk` needs no token: it re-reads the list inside the
write and appends, so a concurrent add cannot be lost.

### `POST /v1/copycat/search`

Fans the query out to the configured targets. A target that cannot be reached at
all is named in `failures`; a target that answered, but not completely, is named
in `warnings` -- and **its hits are in `results`**:

```json
{
  "results": [{"target": "loc", "title": "Gideon the Ninth"}],
  "failures": {"beta": "connection refused"},
  "warnings": {"loc": "partial results: the stream broke after 3 of 9 record(s): XML syntax error"}
}
```

A warning is neither a success nor a failure, and collapsing it into either one
loses something. Treated as a failure, the records that did arrive are thrown
away. Treated as a success -- which is what happened before tasks/258 -- the
client is told a short result set is the whole result set, and copy cataloging
turns entirely on "is my book in this result set?".

Two conditions raise a warning: the record stream broke after delivering some
records, and the search limit truncated it. Whether a broken stream lands on the
first read or the fiftieth is decided by the remote server's page size, so the
two cases must not be reported differently. `/v1/works/{id}/subjects/lookup`
carries the same `warnings` map for the same reason: an empty candidate list
means "no new headings" only if every target answered in full.

Both messages name the advertised result-set size when the target reports one --
`showing 20 of 4113 matches -- refine your search` -- which is why filling a page
is not itself a warning: a target holding exactly 20 records answered completely
(tasks/274). A target that reports no size at all (SRU 2.0 makes the count
optional) still warns on a full page, because "the first 20; there may be more"
is all anyone can honestly say. Note that *no count* and *a count of zero* are
different answers and must not be collapsed.

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
| `GET` | `/v1/readyz` | public | `httpapi.go` |
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
