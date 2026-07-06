# libcatalog Hugo module

Turns a projected `catalog.json` + `facets.json` (from `lcat project`) into a
faceted, accessible, multilingual discovery site: one page per Work, minted by a
content adapter -- no per-record markdown. This is the Tier 1 rendering half of the
framework (ARCHITECTURE §6/§7, `tasks/009`); the graph stays the source of truth
and the JSON is a derived build artifact.

It is a **separate Go module** from the libcatalog framework, so Hugo sites that
import it never pull the Go build dependencies -- it ships only templates and
assets.

**Live reference adopter:** [libcatalog.evefreeman.com](https://libcatalog.evefreeman.com)
("Eve's Library") imports this module, provides projected data, and adds light branding --
a runnable example of a real site built on it, alongside the `exampleSite/` in this repo.

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

   **Custom fields (`extra`).** The adapter maps a fixed set of catalog fields into each
   Work page's params. To surface adopter-specific fields (e.g. a cover URL, rating, or
   read date) without shadowing the adapter, put them under a reserved `extra` object on a
   Work in your projected `catalog.json`; they flow verbatim into the page's params
   (`.Params.<field>`). The fixed set always wins, so `extra` can add keys but never
   clobber a reserved one, and a Work without `extra` is unchanged (tasks/022):

   ```jsonc
   { "id": "w123", "title": "…",
     "extra": { "cover": "https://…/w123.jpg", "rating": 5, "dateRead": "2026-01-15" } }
   ```

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

## Multiple subject vocabularies (tasks/141)

A corpus carrying more than one controlled vocabulary (say Homosaurus + FAST)
facets each in its own sidebar group, and same-label terms from different
vocabularies mint distinct term pages: subject taxonomy keys are
scheme-prefixed slugs (`fast-lesbians`, `homosaurus-lesbians`), from the
`scheme` the v8 projector derives per authority namespace (`lcat project
--subject-scheme` extends the table). Term pages title with the human label
plus the vocabulary ("Lesbians (FAST)"). A single-scheme or scheme-less
corpus renders the one "Subjects" group it always had -- but note that
**re-projecting a multi-scheme corpus changes existing subject term URLs**
(they gain the scheme prefix).

Group order and display names come from the site config (unlisted schemes
follow, labeled by their code):

```toml
[[params.subjectSchemes]]
  scheme = "homosaurus"
  name = "Homosaurus"
[[params.subjectSchemes]]
  scheme = "fast"
  name = "FAST"
```

Like `[taxonomies]`, Hugo does not merge a module's params arrays into the
importing site -- declare the block site-side. `[params] facetLimit` caps the
entries rendered per facet group (default 20), and any group with more than
10 entries gets a client-side type-to-filter box (a substring match over the
rendered entries; no index, no fetch).

## SEO head (default)

The base template ships the SEO basics for every page (tasks/119), so an adopter
gets indexable pages with zero configuration -- and works index as *books*:

- `<meta name="description">` -- page front matter, page params, a synthesized
  "*Title* by *Authors* · *Site*" for Work pages (the `workMetaDescription` i18n
  key, so it localizes), then `[params] description`, in that order.
- Canonical URL plus `hreflang` alternates for every translation.
- Open Graph + Twitter Card -- a Work's `cover` param (via the adapter `extra`
  passthrough) becomes `og:image`; `[params] ogImage` is the site-wide fallback;
  Work pages get `og:type` `book`.
- JSON-LD: `WebSite` with a `SearchAction` into `/works/` on the homepage; `Book`
  on Work pages (authors from contributor roles, per-edition `workExample` with
  format and ISBN, localized subjects as `about`, genre/tags, language).

Set `[params.seo] disable = true` to suppress everything except `<title>`, or
shadow `layouts/_partials/head-seo.html` for finer control. `head-extra.html`
stays the hook for *additions* -- favicons, verification tags, `theme-color`.

## Multilingual

The module is multilingual out of the box -- no per-language content mounts, no copy
of the content adapter (tasks/016).

- **Work pages in every language.** The content adapter calls Hugo's
  `.EnableAllLanguages`, so it mints a full Work-page set for **each** configured
  `[languages]` entry from the single `catalog.json`. Each language's pages localize
  their data: a controlled subject displays `labels[<lang>]`, falling back to `en`
  then the URI. Just declare your `[languages]` -- the module does the rest.
- **UI chrome via i18n tables.** Facet titles, the search form, detail-page section
  headings, and counts come from `{{ i18n }}` keys; the module ships `i18n/en.toml`
  as the defaults. To translate, add `i18n/<lang>.toml` to your site with the same
  keys (see `exampleSite/i18n/es.toml`). Any key you omit falls back to the default
  content language, so a partial table still builds -- **no template fork**.

`exampleSite/` is bilingual (en + es) as a runnable reference: `/works/` renders in
English, `/es/works/` in Spanish, chrome and subject labels included.

Note: taxonomy term-page headings derived from the taxonomy name itself (e.g. the
`Subject:` prefix, the "5 subjects" term count) still use Hugo's taxonomy singular/
plural, which are config-defined, not `i18n` keys -- override `term.html`/`taxonomy.html`
if you need those localized.

## Schema version

Both JSON files carry a top-level `version` (`project.SchemaVersion`). The adapter
fails the build loudly if `catalog.json`'s version does not match the version the
module targets (`params.catalogSchemaVersion`, currently **9**). Reproject with a
matching `lcat` if you hit a mismatch. v6 added the holdings signal: `held` on each
instance and work (physical items, or a live-availability identifier whose feed
still lists the work -- tasks/078). Whether unheld works are hidden, badged, or
faceted is the site's choice; the module renders them unchanged by default. v7
added `summary` on each work (the description/abstract, from `bf:summary` --
tasks/124); the detail page renders it as paragraphs, and Pagefind indexes it.
v8 added `scheme` on each subject and subject facet (the vocabulary code derived
from the authority namespace), driving the per-vocabulary facet groups and
scheme-prefixed subject term keys above (tasks/141). v9 made classifications
`{value, label}` objects: `value` stays the scheme code (the taxonomy key),
`label` the human text when the graph carries one; facets, term pages, and the
detail row show the label and fall back to the code (tasks/142).

## Display labels for language codes

The projector emits languages as raw ISO 639-2 codes; display mapping is the
presentation layer's job (tasks/142). The module ships a LOC code -> English
name table (`data/lcat/languageNames.toml`) consulted by the `lcat-lang-name`
partial everywhere a language renders (facet sidebar, term pages, the detail
row). A site localizes or corrects a name with an i18n key `lang-<code>`
(e.g. `["lang-eng"]` / `other = "Inglés"` in `i18n/es.toml`), which wins over
the shipped table; unknown codes render raw. See `exampleSite/i18n/es.toml`.

## Data quality

Labels are display text; keys and slugs are bounded. Free-text taxonomy values
(subjects, tags, contributor names) are indexed by a key capped at 100 bytes --
longer labels get a truncated key with a short stable hash appended, so one
oversized source label (say, a whole comma-separated artist list in a single
contributor field) cannot mint a term directory past the filesystem's 255-byte
name limit and fail the build. Cards and detail pages always show the full
label; only the term page's URL (and its minted title) reflect the capped key.
Clean the source data if the truncated term pages bother you -- the cap is a
guardrail, not a normalizer.

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
it per provider so a pure-`direct` deployment stays backend-free.

### Physical ILS (DAIA)

The bundled `daia` adapter covers physical holdings via
[DAIA](https://gbv.github.io/daia/) (Document Availability Information API, spoken by
Koha, Sierra/ILS-DI bridges, and the GBV DAIA servers), proving the digital/physical
superset: it populates `locations[]` (per-branch shelf location, call number, status,
and due date) that the digital adapters leave empty.

```toml
  [params.availability.daia]
    transport = "proxied"                        # DAIA endpoints are usually patron/IP-scoped
    proxyUrl  = "https://your-edge-function.example/availability"
    # baseUrl = "https://your-ils.example/daia"  # for a CORS-open DAIA server, use direct instead
```

Editions carry `data-daia-id` (the DAIA document id). The adapter batches ids (a
repeated `id=` query for `direct`, a `{ "provider": "daia", "ids": [...] }` POST for
`proxied`), then maps each DAIA `document`'s holdings to the normalized model: the best
holding wins the overall `status` (`available` when a copy circulates, `holdable` when
it is out but reservable, else `unavailable`), and every holding becomes a
`locations[]` row. A theme renders the one-line summary from `data-status` /
textContent, or the full `data-locations` JSON for a per-branch table. Same proxy
contract as above: the proxy returns the ILS's **raw** `{ document: [...] }` so the
client normalizes identically. Live availability stays out of the graph, so the catalog
still cannot facet or sort by "available now" from the static index (`tasks/004`).

## Search

Two engines, chosen by `[params.search] engine`:

- **Pagefind (recommended).** A static search library that indexes the **built HTML**
  under `public/` *after* Hugo runs -- so the content-adapter-minted Work pages index
  with no markdown and no extra data artifact. It gives real ranked full-text search
  that is **per-language** (it reads the `<html lang>` this module emits and auto-loads
  the matching index, tasks/016), **CJK-capable** (its Extended build segments CJK
  natively), and **facet-filtered** (the Work templates carry `data-pagefind-filter` for
  format, language, subject, tag, contributor, and classification). No custom WASM
  wiring. Enable it and run one post-build step:

  ```toml
  [params.search]
    engine = "pagefind"
  ```
  ```
  hugo --destination public       # build the site
  npm run search:index            # index public/ -> public/pagefind/ (runs npx pagefind)
  # or the standalone binary:  pagefind --site public
  ```

  Pagefind writes its index and drop-in UI into `public/pagefind/` (gitignored along with
  the rest of `public/`). The search partial loads those by URL, so Hugo builds fine
  before the index exists -- the widget simply activates once the index is in place.
  `exampleSite` ships with Pagefind enabled as the reference setup.

- **Interim filter (default when the param is absent).** `assets/lcat-search.js`, a small
  dependency-free client-side substring filter over the rendered list. No post-build
  step; not ranked. This is what an adopter gets out of the box until they opt into
  Pagefind.

Either way, **with JavaScript disabled the search form still submits to `/works/`** and
the full faceted list browses -- search never blocks navigation.

### Advanced: roaringrange

For very large corpora, custom BM25 ranking internals, split-set sharding, or a
**no-Node** build, `roaringrange` (Go build-side indexes + a WASM reader) is the opt-in
advanced engine. Its build-side index arms exist (`tasks/005`/`010`); the browser reader
(`tasks/009`) is the remaining advanced-path work. Pagefind covers the multilingual/CJK
goal natively for the default path, so most deployments will not need it.

## Accessibility

Accessibility is a first-class goal (ARCHITECTURE §6/§7): semantic landmarks, a skip
link, labeled search, `role="status"` result counts, and ordered headings. A repeatable
axe-core (WCAG 2.1 A/AA) audit ships as dev tooling -- Hugo never consumes it:

```
cd exampleSite && hugo --destination public
cd .. && npm install && npm run test:a11y   # audits every built page, exits non-zero on a violation
```

`npm run test:js` runs the availability adapter's unit tests. `npm run test:links`
walks the built site and asserts every internal facet/term/work link resolves to a
generated page -- guarding the URL-safe facet slugs (facet and term links are slugified
through one shared function so a subject/tag label with `+`, `/`, etc. never 404s on a
CDN that mis-decodes those in a path; tasks/023). `color-contrast` is
excluded from the automated run (jsdom has no layout) -- verify it in a real browser
(Lighthouse / axe DevTools), and re-check contrast whenever you override `assets/lcat.css`.

`npm run search:index` builds the optional Pagefind index over `exampleSite/public`
(see "Search"). Like the a11y audit, it is optional post-build tooling -- Hugo never
consumes it.

## Theming

The default theme is desktop-credible out of the box and **token-driven**, so the usual
way to re-brand is to re-set the `--lcat-*` custom properties in your own stylesheet
(loaded after the module's `lcat.css`) -- every component re-themes from them, with no CSS
fork (tasks/025):

| Token | Role |
| --- | --- |
| `--lcat-fg` / `--lcat-bg` | body ink / page background |
| `--lcat-accent` | links, buttons, active states (keep >=4.5:1 on `--lcat-bg`) |
| `--lcat-accent-ink` | darker accent for headings / large text |
| `--lcat-muted` | secondary text, counts |
| `--lcat-border` | hairlines, card/panel borders |
| `--lcat-surface` / `--lcat-surface-alt` | raised cards & facet panel / chips & subtle fills |
| `--lcat-radius`, `--lcat-shadow` | corner radius, elevation |
| `--lcat-gap`, `--lcat-maxw` | layout column gap, max content measure |

```css
/* your-site/assets/site.css, loaded after lcat.css */
:root { --lcat-accent: #115c52; --lcat-bg: #fbf9f4; --lcat-maxw: 72rem; }
```

### Buttons

CTA buttons are easy to get wrong across light/dark (the token *pairs* matter), so
the module ships a `.lcat-btn` component whose three variants carry mode-correct
pairs -- verified once here instead of per adopter (tasks/120). Works on `<a>` and
`<button>`:

| Variant | Pairing | Use on |
| --- | --- | --- |
| `.lcat-btn--solid` | `--lcat-accent` fill, `--lcat-on-accent` text | any background |
| `.lcat-btn--surface` | `--lcat-surface` fill, `--lcat-accent` text, `--lcat-border` border | page/neutral backgrounds |
| `.lcat-btn--ghost` | transparent, `--lcat-on-accent` text + border | accent-filled surfaces (a hero) |

```html
<a class="lcat-btn lcat-btn--solid" href="/works/">Browse the catalog</a>
```

Re-setting the tokens re-themes the buttons with everything else; keep
`--lcat-on-accent` readable on `--lcat-accent` in both modes if you change either.

### Cover art (optional)

Set `[params] covers = true` and result cards + Work detail pages render a cover slot from
each Work's `cover` param -- supply it via the adapter `extra` passthrough (see "Provide the
projected data") as an `https://` image URL. A Work without a cover shows a graceful lettered
placeholder in the list. Covers are **off by default**, so a catalog without cover art is
unchanged.

## Overriding

Everything is a plain template or asset, so a site or theme layers cleanly on top:
shadow `layouts/_partials/work-card.html`, `assets/lcat.css`, etc. from the project
root and Hugo's module precedence uses yours.

### Injection hooks (no `baseof.html` copy)

To add site-wide chrome without shadowing the base template, override these
empty-by-default hooks. They add nothing to the output until you do, so a site that
ignores them is byte-for-byte unchanged (tasks/020, tasks/118):

- **`layouts/_partials/head-extra.html`** -- injected into `<head>` after the module
  stylesheet. Add favicons, extra meta, or verification tags here (the SEO basics --
  description, canonical, Open Graph, JSON-LD -- ship by default; see "SEO head").
- **`layouts/_partials/banner.html`** -- rendered first in `<body>` after the skip
  link, ABOVE the header: a site-wide ribbon (demo disclosure, closure notice,
  emergency message). Wrap the content in a landmark (e.g. `<aside aria-label="…">`).
- **`layouts/_partials/brand.html`** -- the content of the header's `.lcat-brand`
  anchor (defaults to the site title). Shadow it to put a logo or colophon next to
  the title; keep decorative images `aria-hidden`.
- **`layouts/_partials/footer.html`** -- rendered after the main layout, before the
  deferred scripts. Add a site-wide footer.
- **`layouts/_partials/work-extra.html`** -- rendered on the Work detail page after
  the metadata list, inside the Pagefind-indexed article (tasks/125). Override it to
  render your adopter passthrough extras (tasks/022) -- e.g. a personal reading log's
  `.Params.rating` / `.Params.dateRead` -- without shadowing `page.html`.
- **`hero` block** -- a full-width slot between the header and the faceted layout,
  filled by a layout `define` (e.g. an intro on the home page):

  ```
  {{ define "hero" }}<section class="lcat-hero">…</section>{{ end }}
  ```

**Header navigation** needs no hook at all: define Hugo's `[[menu.main]]` in your
site config and the module renders an accessible primary nav between the brand and
the search box (`aria-current` on the active entry; a section landing such as
`/works/` stays active on its child pages; the `primaryNav` i18n key labels the
landmark). Per-language menu names come from `[languages.<lang>.menus.main]`. A site
with no main menu gets no nav markup. Shadow `layouts/_partials/nav.html` to change
the markup itself.

Overriding a hook never requires copying `baseof.html`, so a module bump stays
merge-free.

## Large catalogs (build performance)

Measured against the first ~50k-work consumer (48,515 works / bilingual /
10k subject + 45k contributor terms; Apple Silicon laptop, hugo 0.148):

**What the module does for you (tasks/133).** The facet sidebar renders once
per language (`partialCached` -- it used to re-render on every list and term
page: 4 of 11 template-minutes at 10k works), each work card renders once per
work per language keyed on its URL (a card is identical on every pager and
term page it appears on), and the page-invariant header partials (search box,
theme toggle) are cached too. At 10k works / one language this cut a clean
build from 71s to 57s wall and 168 to 107 CPU-seconds; template time now goes
to page volume itself, not repeated partials.

**If you shadow `facets.html` or `work-card.html`**, keep them cacheable: the
facet sidebar may read only facets.json/site/i18n (never page state), and a
card's output must be a function of the work page alone. A shadow that reads
the current page renders stale content under the cache.

**Where the rest goes.** A CPU profile of the cached build is ~40% file-write
syscalls, ~23% Hugo's content-tree bookkeeping, ~10% GC -- all proportional to
page/file volume, not template work. At 10k works one language emitted 71,727
files: 10k work pages plus 43k taxonomy term/pager pages plus 18.4k RSS
feeds. Volume is the lever.

**Site-side levers, biggest first (measured at 10k works, one language):**

- **Trim facet dimensions.** Every `[taxonomies]` entry mints a term page per
  distinct value per language -- contributors alone were 45k term pages per
  language at full scale. Dropping the contributor/classification/language
  dimensions took the build from 57s / 162 CPU-s / 71.7k files to **21s / 65
  CPU-s / 25k files**; the data stays in catalog.json and on work pages
  either way. CAUTION: Hugo *merges* `[taxonomies]` across `--config` files
  -- an override file cannot remove a dimension; trim the block in your main
  site config.
- **Drop RSS if you don't want it** (18.4k files here). In your site config:

  ```toml
  [outputs]
    home = ["html"]
    section = ["html"]
    taxonomy = ["html"]
    term = ["html"]
  ```

- **Diagnose before tuning:** `hugo --templateMetrics --templateMetricsHints`
  names the partials that dominate (a 100%-cache-potential partial with big
  cumulative time is a `partialCached` candidate -- upstream that finding
  rather than shadowing, please); `hugo --profile-cpu cpu.prof` + `go tool
  pprof` for what's left. Wall-clock on a laptop is noisy at this file volume
  -- trust CPU seconds and file counts.
- **CI sharding:** `--renderSegments` can split a huge render across jobs
  (segment per language is the natural cut).

## Integration points still stubbed

- **Advanced search reader** -- Pagefind is the shipped, recommended engine (see
  "Search"), and the no-config fallback is `assets/lcat-search.js`, a client-side
  substring filter. The remaining stub is the opt-in advanced path: the
  roaringrange WASM reader over its build-side indexes (`tasks/009`/`010`), for
  deployments that outgrow Pagefind.
