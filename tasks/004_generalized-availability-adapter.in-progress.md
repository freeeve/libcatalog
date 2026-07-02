# 004 -- Generalized availability adapter (all library sources)

## Catalog-side contract (done in libcatalog)

The libcatalog half is in place: `catalog.json` (schema v2) now carries each
non-ISBN Instance identifier as `{source, value}` (`project.ProviderID`), sourced
from the grain's `bf:source` (`tasks/008`). So an adapter configured with an
`idField` picks its key by scheme -- OverDrive keys on `source == "overdrive-reserve"`
(the title id is `"overdrive"`) -- e.g.
`inst.providerIds.filter(p => p.source === idField).map(p => p.value)`. Everything
below is the client-side adapter + optional proxy, which live in the Hugo-module /
deployment repo, not libcatalog; per the cross-repo boundary they need a task filed
there.

## Problem
Live availability is fetched client-side at view time and kept out of the graph
(ARCHITECTURE.md §5). OverDrive's Thunder API exposes an *unauthenticated*
availability check, so a `direct` browser call is viable for it. But every
library source differs in transport, auth, id scheme, and digital-vs-physical
semantics. Rather than hard-code OverDrive, generalize availability into a
per-provider adapter -- the runtime sibling of the ingest provider (§9) -- so any
source plugs in behind one normalized model.

## Normalized availability model
The shape every adapter maps to (UI renders only this):

```
Availability {
  provider:         string          // "overdrive"
  status:           "available" | "unavailable" | "holdable" | "unknown"
  format?:          string          // ebook | audiobook | physical | ...
  copiesOwned?:     number
  copiesAvailable?: number
  holdsCount?:      number
  estimatedWaitDays?: number
  locations?: [ { library, callNumber, status, dueDate } ]   // physical ILS
  actionUrl?:       string          // borrow / place-hold / catalog deep link
  fetchedAt:        timestamp
}
```

## Adapter interface (client-side)
```
AvailabilityAdapter {
  providerKey: string
  idField:     string                       // which feed:<provider> id it keys on
  transport:   "direct" | "proxied"         // build knows if a proxy is required
  auth:        "none" | "public-key" | "scoped-token"
  fetch(ids: string[], cfg): Promise<Record<id, Availability>>   // batch-first
}
```

## Scope
1. **Reference adapter: OverDrive/Thunder**, `direct` + `none`. Confirm CORS for
   the deployment origin; if headers are not permissive, fall back to `proxied`
   (same interface, no UI change).
2. **Physical ILS adapter** (Koha/Sierra via DAIA or ILS-DI), `proxied` +
   scoped-token, populating `locations[]`. Proves the digital/physical superset.
3. **Proxy contract.** A thin, stateless edge/serverless function the framework
   ships as an optional artifact: forwards a batch id list to a source, strips
   secrets, normalizes. Deployments enable it only for `proxied` providers, so
   the pure-static Tier 1 path stays backend-free when every provider is
   `direct`.
4. **Batching + client cache.** One call per provider per results page; short
   TTL; in-flight de-dup. Degrade to `status: unknown` on error/timeout.
5. **Provider feasibility matrix.** Document, per source: CORS? auth mode? batch
   endpoint? rate limits? -> chosen transport. Start with OverDrive; add
   Boundless/Axis 360, hoopla, cloudLibrary, and a physical ILS.

## Known trade-off (from review #3)
Because availability is out of the graph and fetched at view time, the catalog
**cannot facet or sort by "available now"** from the static index. If a
deployment needs that, the mitigation is a periodically-refreshed availability
**sidecar** (not committed to the graph, regenerated on a schedule) that the
projector can fold into the index for a *coarse* availability facet -- explicitly
stale, distinct from the live per-view number. **Decision (2026-07-02): Tier 2 add-on,
not Tier 1.** A scheduled refresh job is backend-ish and cuts against Tier 1's
backend-free pitch (ARCHITECTURE §6); Tier 1 keeps availability live/per-view only, and a
deployment that needs an "available now" facet opts into the sidecar at Tier 2.

## Acceptance
- [x] OverDrive availability renders from a `direct` adapter with no committed
  secret; proxy fallback produces identical normalized output (client-side `proxied`
  transport delivered; the proxy *function* is a deployment artifact -- see Remaining).
- [ ] One physical-ILS adapter renders `locations[]` through the proxy.
- [x] A results page issues one batched call per provider, caches within TTL, and
  never blocks render on a failed fetch.
- [x] Published feasibility matrix covering at least OverDrive + one physical ILS.
  See `docs/availability-providers.md` (OverDrive/Thunder verified; DAIA/ILS-DI physical
  ILS; digital vendors reasoned + flagged for per-deployment CORS/auth verification).

## Delivered -- OverDrive reference adapter (commit `45f5acd`)

The catalog-side contract was already in place; this adds the client-side half in the
Hugo module (in-repo), grounded in the real Thunder API (verified against deeplibby's
`overdrive_client.go`):

- **`hugo/assets/lcat-availability.js`** -- the normalized model + adapter registry +
  the OverDrive/Thunder `direct` adapter (auth none). It reads `data-overdrive-reserve`
  off each edition, batches ids (<=25, Thunder's cap), `POST`s to
  `/libraries/{slug}/media/availability`, maps `{ownedCopies, availableCopies,
  holdsCount|holdsPlaced, estimatedWaitDays, availabilityType}` to `{status:
  available|holdable|unavailable|unknown, copies*, holdsCount, estimatedWaitDays,
  actionUrl}`, caches with a short TTL, de-dups in-flight requests, and degrades to
  `unknown` on error/timeout (AbortController) -- never blocking render. A new source
  plugs in via `registerAdapter({providerKey, domAttr, batchSize, fetchBatch})`, the
  runtime sibling of an ingest provider (`tasks/006`).
- **Wiring** -- `baseof.html` emits the config as JSON and loads the script only when
  the site sets `[params.availability] enabled=true`; `page.html` renders each
  edition's status placeholder + `data-overdrive-reserve`/`data-format` hooks;
  `lcat.css` colors by `data-status`. README documents the config; exampleSite ships
  it disabled (no external calls by default).
- **Tests** -- `hugo/availability_test.cjs` (node, no DOM/network, injected fetch): 14
  cases over status mapping, field normalization, batching (>25 -> multiple calls),
  cache hit, in-flight de-dup, error/non-2xx degradation, and `readConfig` (incl. the
  Hugo module-context double-encoded-JSON quirk). Validated the real Hugo build output
  parses through `readConfig` to a usable config.

## Delivered -- proxied transport (commit pending)

Client-side `proxied` transport added to `lcat-availability.js`: `overdriveRequest`
splits the batch URL/body by `transport` -- `direct` hits Thunder, `proxied` POSTs
`{provider, slug, ids}` to a configured `proxyUrl`. The proxy is a thin forwarder that
returns the source's **raw** `{items}` response, so the client's shared normalize path
yields **identical** models either way (proven by a node test: `resolve` over the same
availability via both transports produces `deepEqual` models, to different URLs).
Config: `[params.availability.overdrive] transport = "proxied", proxyUrl = "..."`.
Proxy contract documented in `hugo/README.md` (## Direct vs proxied transport);
exampleSite carries the commented option. 17 availability tests pass.

## Delivered -- provider feasibility matrix (commit pending)

`docs/availability-providers.md` publishes the per-provider transport/auth/batch/CORS
matrix with confidence markers: OverDrive/Thunder verified (unauth, `<=25` batch, real
endpoint) with CORS flagged for per-origin checking; DAIA and ILS-DI/PAIA physical ILS
(proxied, scoped-token, `locations[]`); hoopla / Boundless(Axis 360) / cloudLibrary
reasoned as proxied patron-authed. It documents how the transport choice flows to the
adapter and the digital-vs-physical superset. Satisfies acceptance item 4 (OverDrive +
one physical ILS); the digital-vendor cells are marked for per-deployment verification.

## Remaining (deferred)

- **The proxy *function*** -- a stateless edge/serverless handler that receives
  `{provider, slug, ids}`, calls the source, strips secrets, and returns the raw
  response. This is a deployment-repo artifact (the client contract is now defined and
  tested); ship it only for `proxied` providers so pure-`direct` stays backend-free.
- **Physical-ILS adapter** (DAIA / ILS-DI) populating `locations[]` -- proves the
  digital/physical superset; needs the proxy.
- **Feasibility matrix** (CORS/auth/batch/rate-limit per source: OverDrive,
  Boundless/Axis 360, hoopla, cloudLibrary, a physical ILS).
- **Coarse "available now" facet sidecar** (§ Known trade-off) -- **decided: Tier 2
  add-on** (2026-07-02), out of scope for Tier 1's backend-free path.
- **Live CORS check** against a real deployment origin (can't verify here); if Thunder
  is not permissive from the site origin, flip that provider to `proxied`.
