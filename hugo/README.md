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
     contributor = "contributors"
     classification = "classifications"
   ```

That is the whole setup -- see `exampleSite/` for a runnable reference.

## What it renders

- `/` and `/works/` -- a paginated, faceted list of Works.
- `/works/<id>/` -- a Work detail page: contributors, subjects (linked), languages,
  classifications, and its Instances/editions.
- `/languages/`, `/subjects/`, `/contributors/`, `/classifications/` and their term
  pages -- the facet navigation, with counts from `facets.json`.

## Schema version

Both JSON files carry a top-level `version` (`project.SchemaVersion`). The adapter
fails the build loudly if `catalog.json`'s version does not match the version the
module targets (`params.catalogSchemaVersion`, currently **2**). Reproject with a
matching `lcat` if you hit a mismatch.

## Overriding

Everything is a plain template or asset, so a site or theme layers cleanly on top:
shadow `layouts/_partials/work-card.html`, `assets/lcat.css`, etc. from the project
root and Hugo's module precedence uses yours.

## Integration points still stubbed

- **Search** -- the search box is wired to `assets/lcat-search.js`, an interim
  client-side substring filter (progressive enhancement). It will be replaced by
  the roaringrange WASM reader over the `search-manifest.json` index (`tasks/010`).
- **Availability** -- each edition carries `data-instance` and, for OverDrive,
  `data-overdrive-reserve` (the scheme-tagged Reserve ID from catalog.json v2). A
  client-side availability adapter (`tasks/004`) reads these to fetch live
  availability at view time; until one is wired the placeholder stays inert.
