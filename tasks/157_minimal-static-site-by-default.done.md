# 157 -- Minimal static site by default: details + browse shell + sitemap

## Outcome (2026-07-07): shipped as the documented "minimal static profile"

Implemented as **opt-in native Hugo config**, not a changed module default --
taxonomies are configured by each consuming site (`[taxonomies]` +
`[[menu.main]]`), so the module cannot flip them without breaking existing
deployments, and per the prefer-native-formats convention the switch is Hugo's
own `disableKinds = ["taxonomy", "term"]` rather than an invented module flag.
The module's job was to make that profile work cleanly, and it does:

- **Verified on the exampleSite**: `disableKinds = ["taxonomy","term"]` +
  `engine = "roaringrange"` builds 117 pages -> **15** (details + `/works/`
  shell + home + sitemaps); detail pages stay in the per-language sitemaps
  (crawlable); the module emits **zero dead term links** (the only dangling
  link was the exampleSite's own menu entry, a site-config concern, documented);
  Playwright E2E 5/5 (search, facet-only browse, query+facet, restore) against
  the minimal build; a11y clean.
- **Documented** in `hugo/README.md` ("Minimal static profile", with the
  roaringrange engine section it builds on).
- The `/works/` list keeps its static pagination as the no-JS path; curated
  static views for SEO are task 160.

Original framing below.

Plane 2 default of [154]. Shrinks the public build from "pre-render every view"
to the finite, canonical, incrementally-regenerable set; the combinatorial
surface moves client-side (task 158).

## Rationale

Facet / search / filtered-browse views have no finite pre-renderable set, and
pre-rendering the corpus x facet combinations is exactly what makes the site
build slow (slow even on a laptop today) and non-incremental. Detail pages, by
contrast, are canonical and embarrassingly incremental (a work changes ->
regenerate one page, no cross-page dependency).

## Default output

- **One detail page per work** -- the canonical, crawlable `/works/<id>` URL.
- **One browse shell** -- the entry page that boots the client-side reader (task
  158). Optionally static-render the *first page* of the default browse for fast
  first paint + SEO, then let JS hydrate.
- **A sitemap** linking every detail page -- so crawlers still reach all details
  even though browse/search are JS. This is the one thing that must stay, or the
  details become undiscoverable.

Stop emitting the per-facet / per-search / per-browse-combination pages by
default.

## Touches

`hugo/` module templates and whatever in `project/` currently enumerates
facet/browse pages. Preserve the existing per-facet output behind the opt-in in
task 160 -- do not delete the capability, change the default.

## Tradeoffs (accepted)

- SEO: covered by static details + sitemap; results pages need no indexing.
- No-JS: browse/search degrade to details + shell (+ optional static first
  page). Acceptable for the catalog audience.

## Verify

- A default build emits N detail pages + shell + sitemap and no per-combination
  pages.
- Every detail page is reachable from the sitemap.
- Build time scales with changed works (with task 159), not total views.
