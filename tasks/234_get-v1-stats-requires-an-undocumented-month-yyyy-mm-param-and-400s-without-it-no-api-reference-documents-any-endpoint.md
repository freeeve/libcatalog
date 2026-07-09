# 234 -- GET /v1/stats requires an undocumented month=YYYY-MM param and 400s without it; no API reference documents any endpoint

Filed from libcat on 2026-07-09 (cross-repo ask).

## Answering the question that came with it: this is an admin feature, not OPAC.

`/v1/stats` is mounted `librarian`-gated in `backend/httpapi/review_handlers.go:178`,
alongside the review queue. It reports **editing activity from the audit log** for
one month, not anything about the collection:

```
GET /v1/stats?month=2026-07   (librarian)
{"month":"2026-07","total":9,"actors":1,"works":1,
 "byAction":{"COPYCAT_COMMIT":2,"COPYCAT_STAGE":2,"WORK_RELATE":3,…}}
```

Those counts are staff actions. The only consumer is the staff Dashboard
(`backend/ui/src/screens/Dashboard.svelte:88`, `fetchStats(activityMonth)`).
Nothing in the public catalog reads it, and an anonymous request is refused:

```
anon        GET /v1/stats            -> 401 {"error":"missing bearer token"}
```

So it belongs to the admin surface. Filing it as such.

## Symptom

The `month` parameter is required, and there is no way to discover that short of
reading the handler:

```
librarian   GET /v1/stats                  -> 400 {"error":"month must be YYYY-MM"}
librarian   GET /v1/stats?month=bogus      -> 400 {"error":"month must be YYYY-MM"}
librarian   GET /v1/stats?month=2026-07    -> 200 {"month":"2026-07","total":9,…}
```

The 400 is well-formed and names the format, which is better than most. But a
caller has to guess that a parameter exists at all: the bare endpoint gives no
hint that `month` is what is missing rather than, say, a body or a role.

More to the point, **no document anywhere describes this endpoint or any other**.
`docs/` holds ARCHITECTURE, ROADMAP, marc-fidelity, authority-sources,
availability-providers, hardcover-provider, build-pipeline -- and no API
reference. `grep -rl "GET /v1/" docs/ README.md` returns nothing. The only prose
describing `/v1/stats` is inside a closed task file, `tasks/093:21`.

## Root cause

Not a code defect. `review_handlers.go:179-183` validates correctly:

```go
month := r.URL.Query().Get("month")
if !monthPattern.MatchString(month) {
    writeError(w, http.StatusBadRequest, "month must be YYYY-MM")
    return
}
```

The gap is that the parameter is mandatory with no default, and that the HTTP
surface as a whole is undocumented outside the handlers.

## Why it matters

An HTTP API with no reference is only usable by people who read Go. That is fine
while the only client is the bundled SPA, and it stops being fine the moment a
library wants to pull its own editing statistics into a report, or a second
client appears. `/v1/stats` is where I noticed it; the gap is the surface.

The narrower ergonomic point stands on its own: a dashboard statistic keyed to a
month almost always wants "this month" as its default. Requiring the parameter
buys nothing and costs every caller a round trip to a 400.

## Expected

Two separable pieces; the first is cheap and the second is the real ask.

1. `GET /v1/stats` with no `month` defaults to the current month (server clock,
   UTC), rather than 400ing. An explicitly malformed `month` still 400s -- the
   distinction is between *absent* and *wrong*. If a default is unwanted, the
   error should at least say the parameter is required and show an example:
   `month is required, e.g. month=2026-07`.

2. An API reference under `docs/` enumerating the `/v1` surface: path, method,
   required role, parameters, response shape. It does not need to be
   hand-maintained prose -- the routes are all registered through `mux.Handle`
   with a role wrapper, so a generated table is plausible. Whatever the form, it
   should be checked against the router so it cannot drift.

## Repro

```
curl -s -H "Authorization: Bearer $TOKEN" localhost:8481/v1/stats            # 400
curl -s -H "Authorization: Bearer $TOKEN" localhost:8481/v1/stats?month=2026-07  # 200
```

`harness/retest.mjs` carries the check as `t234`: it asserts the bare call
returns 200 with a `month` field equal to the current UTC month, and that a
malformed `month` still returns 400. It is a read-only check -- `/v1/stats`
writes nothing.
