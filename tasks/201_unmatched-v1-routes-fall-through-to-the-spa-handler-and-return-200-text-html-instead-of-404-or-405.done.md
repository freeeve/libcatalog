# 201 -- unmatched v1 routes fall through to the SPA handler and return 200 text-html instead of 404 or 405

Filed from libcat-e2e on 2026-07-09 (cross-repo ask).

## Symptom

Any `/v1/…` request that does not match a registered route -- unknown path *or*
wrong method on a known path -- is served by the SPA handler and comes back
**200 `text/html`**:

```
DELETE /v1/authorities/aaaaaaaaaaaaaa     -> 200 text/html   (no such route)
GET    /v1/totally-not-a-route            -> 200 text/html   (no such route)
DELETE /v1/works/w0cfnsjg6micju           -> 200 text/html   (wrong method)
POST   /v1/healthz                        -> 200 text/html   (wrong method)
PUT    /v1/macros                         -> 200 text/html   (wrong method)
```

An API client cannot tell "you deleted it" from "that endpoint does not exist".

## Root cause

`backend/httpapi/httpapi.go:191`

```go
if deps.UI != nil {
	mux.Handle("/", deps.UI)
}
```

`"/"` is `http.ServeMux`'s catch-all. Go 1.22 method-aware patterns only produce
a 405 when *some* pattern matches the path but not the method **and** no broader
pattern matches; the `"/"` registration always matches, so every unmatched `/v1/`
request is handed to the SPA, which serves `index.html` with 200.

## Why it matters

- **It hides client bugs and makes them look like successes.** This is not
  hypothetical: the libcat-e2e harness called `DELETE /v1/authorities/{id}`
  (a route that does not exist), read `status < 300`, and cheerfully reported
  `deleted=2` while nothing had been deleted. A real integration would ship the
  same mistake.
- Any client that `JSON.parse`es a `/v1/` response now parses an HTML document
  and throws a confusing syntax error instead of seeing a clean 404.
- It defeats method-not-allowed diagnostics across the whole API surface, and it
  makes route typos invisible in logs (they log as `200`).
- Reverse proxies, uptime checks and generated API clients all treat 200 as
  healthy.

## Expected

`/v1/` is an API namespace and should never be served by the SPA. Register an
explicit terminal handler for it *before* the catch-all:

```go
mux.HandleFunc("/v1/", func(w http.ResponseWriter, r *http.Request) {
	writeError(w, http.StatusNotFound, "no such endpoint")
})
if deps.UI != nil {
	mux.Handle("/", deps.UI)
}
```

`"/v1/"` is more specific than `"/"`, so ServeMux prefers it, and real routes
(`GET /v1/works`, …) are more specific still and keep winning. Wrong-method
requests to a known path then land on this handler; returning 404 is acceptable,
or track the known paths to return a proper 405 with `Allow`.

Guard it with a test asserting `GET /v1/totally-not-a-route` -> 404
`application/json`, and `POST /v1/healthz` -> 404/405, not 200 `text/html`.

## Repro

```sh
# libcat-e2e
node harness/probe_selfmerge.mjs   # prints the unmatched-route table above
```

Reproduces identically whether the embedded SPA is the real build or the
`lcat-ui-placeholder` page -- the catch-all is the cause, not the asset.

## Outcome

Fixed in ed9ec0d, released v0.51.0, per your sketch: with the SPA
mounted, a terminal /v1/ handler answers {"error":"no such endpoint"}
as JSON 404 before the catch-all. One deliberate wrinkle your report
anticipated: a catch-all preempts ServeMux's native 405, so
wrong-method requests to known paths answer 404 here (your "returning
404 is acceptable" option; route-table tracking for a proper Allow
header noted as not-worth-it-yet). UI-LESS deployments keep the mux's
native 404/405 -- the pre-existing TestUnknownRouteAndMethod pins
that, and the new TestV1NeverFallsThroughToSPA pins the mounted case
(4 probe shapes -> 404 application/json with an error body; /admin/
still reaches the SPA). Verified live against the playground: all four
of your probe rows now 404 application/json.
