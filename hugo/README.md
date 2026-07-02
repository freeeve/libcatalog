# libcatalog Hugo module

Turns a projected `catalog.json` + `facets.json` (from `lcat project`) into a
faceted, accessible, multilingual discovery site: one page per Work, minted by a
content adapter -- no per-record markdown. This is the Tier 1 rendering half of the
framework (ARCHITECTURE §6/§7, `tasks/009`); the graph stays the source of truth
and the JSON is a derived build artifact.

It is a **separate Go module** from the libcatalog framework, so Hugo sites that
import it never pull the Go build dependencies -- it ships only templates and
assets.

## Requirements

- Hugo **extended, >= 0.146** (content adapters + the flat template system).

## Setup

1. **Import the module** in your site config:

   ```toml
   [module]
     [[module.imports]]
       path = "github.com/freeeve/libcatalog/hugo"
   ```

   Then `hugo mod get github.com/freeeve/libcatalog/hugo`.

2. **Provide the projected data** under the site's `assets/`:

   ```
   lcat project --catalog catalog.nq --out assets/
   ```

   so that `assets/catalog.json` and `assets/facets.json` exist. The adapter reads
   them with `resources.Get` (not `.Site.Data`), so the corpus is not pinned in
   global site data.

3. **Declare the facet taxonomies** in your site config. Hugo does **not** merge a
   module's `[taxonomies]`, so this block is required in the site (copy it verbatim):

   ```toml
   [taxonomies]
     language = "languages"
     subject = "subjects"
     tag = "tags"
     format = "formats"
     contributor = "contributors"
     classification = "classifications"
   ```

That is the whole setup -- see `exampleSite/` for a runnable reference.

## What it renders

- `/` and `/works/` -- a paginated, faceted list of Works.
- `/works/<id>/` -- a Work detail page: contributors, controlled subjects (label +
  authority link), genre/tags, formats, languages, classifications, and its
  Instances/editions (each labeled with its format).
- `/languages/`, `/subjects/`, `/tags/`, `/formats/`, `/contributors/`,
  `/classifications/` and their term pages -- the facet navigation, with counts from
  `facets.json`. Controlled subjects (authority URIs, e.g. Homosaurus) are a distinct
  dimension from uncontrolled feed genre tags; subjects display their resolved label.
  Format (ebook / audiobook) is a per-Instance property, so a Work that clusters an
  ebook and an audiobook edition appears under both formats (tasks/011).

## Schema version

Both JSON files carry a top-level `version` (`project.SchemaVersion`). The adapter
fails the build loudly if `catalog.json`'s version does not match the version the
module targets (`params.catalogSchemaVersion`, currently **5**). Reproject with a
matching `lcat` if you hit a mismatch.

## Live availability (optional)

Availability is fetched client-side at view time by `assets/lcat-availability.js` and
kept out of the graph (ARCHITECTURE §5), so the static build stays backend-free. It is
**off unless the site configures it**. To enable the bundled OverDrive/Thunder adapter:

```toml
[params.availability]
  enabled = true
  [params.availability.overdrive]
    slug = "your-overdrive-library-slug"   # e.g. the {slug}.overdrive.com subdomain
    # baseUrl / actionUrlTemplate / timeoutMs are optional overrides
```

Each edition carries `data-instance` and, for OverDrive, `data-overdrive-reserve` (the
scheme-tagged Reserve ID from `catalog.json`). The adapter batches those ids (<=25 per
call), POSTs to Thunder's public `/libraries/{slug}/media/availability`, normalizes to
`{ status: available | holdable | unavailable | unknown, copiesOwned, copiesAvailable,
holdsCount, estimatedWaitDays, actionUrl }`, caches briefly, de-dups in-flight requests,
and fills each `.lcat-availability` placeholder. A failed or slow fetch degrades to
`unknown` (blank) and never blocks render; with the config absent the placeholder stays
inert. A new source plugs in via `registerAdapter({ providerKey, domAttr, batchSize,
fetchBatch })` -- the runtime sibling of an ingest provider (`tasks/006`).

### Direct vs proxied transport

If the source's CORS does not permit a browser call from your deploy origin (or you
want to keep the source behind an edge function), switch that provider to a proxy:

```toml
  [params.availability.overdrive]
    transport = "proxied"
    proxyUrl  = "https://your-edge-function.example/availability"
    slug      = "your-overdrive-library-slug"   # still used for the borrow deep link
```

**Proxy contract.** With `transport = "proxied"` the adapter POSTs each batch to
`proxyUrl` as `{ "provider": "overdrive", "slug": "...", "ids": [...] }` instead of
calling the source directly. The proxy is a thin, stateless forwarder: it uses
`provider`+`slug` to call the source, strips any secrets, and returns the source's
**raw response verbatim** (OverDrive: `{ "items": [...] }`). Because the client
normalizes that same response either way, a proxied fetch yields **identical** models
to a direct one -- the only difference is the URL. The proxy function itself is a
deployment artifact (an edge/serverless handler), not shipped by this module; enable
it per provider so a pure-`direct` deployment stays backend-free. A physical-ILS
adapter (DAIA/ILS-DI, populating `locations[]`) is future work (`tasks/004`).

## Accessibility

Accessibility is a first-class goal (ARCHITECTURE §6/§7): semantic landmarks, a skip
link, labeled search, `role="status"` result counts, and ordered headings. A repeatable
axe-core (WCAG 2.1 A/AA) audit ships as dev tooling -- Hugo never consumes it:

```
cd exampleSite && hugo --destination public
cd .. && npm install && npm run test:a11y   # audits every built page, exits non-zero on a violation
```

`npm run test:js` runs the availability adapter's unit tests. `color-contrast` is
excluded from the automated run (jsdom has no layout) -- verify it in a real browser
(Lighthouse / axe DevTools), and re-check contrast whenever you override `assets/lcat.css`.

## Overriding

Everything is a plain template or asset, so a site or theme layers cleanly on top:
shadow `layouts/_partials/work-card.html`, `assets/lcat.css`, etc. from the project
root and Hugo's module precedence uses yours.

## Integration points still stubbed

- **Search** -- the search box is wired to `assets/lcat-search.js`, an interim
  client-side substring filter (progressive enhancement). It will be replaced by
  the roaringrange WASM reader over the `search-manifest.json` index (`tasks/010`).
