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
stale, distinct from the live per-view number. Decide whether this is in scope
for Tier 1 or a Tier 2 add-on.

## Acceptance
- OverDrive availability renders from a `direct` adapter with no committed
  secret; proxy fallback verified to produce identical normalized output.
- One physical-ILS adapter renders `locations[]` through the proxy.
- A results page issues one batched call per provider, caches within TTL, and
  never blocks render on a failed fetch.
- Published feasibility matrix covering at least OverDrive + one physical ILS.
