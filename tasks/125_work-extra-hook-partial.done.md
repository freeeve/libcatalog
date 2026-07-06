# 125 -- Add a work-extra.html hook partial to the Hugo module

Adopter passthrough extras (tasks/022, 026) reach Work page params, but the
module renders only `cover`; anything else (e.g. the demo's Hardcover
`rating`/`dateRead` reading-log fields) is silently dropped, and the only
adopter recourse is shadowing all of `page.html`. Personal-log fields do not
belong in the generic module (institutional catalogs have no "rating"), so
extend the existing hook pattern (head-extra, banner, brand -- tasks/118-120)
to the Work detail page:

- New `hugo/layouts/_partials/work-extra.html`: empty by default, documented as
  the adopter hook for rendering site-specific extras.
- Call it from `page.html` with the page context, positioned after the meta
  `<dl>` and before the editions section (adjust if the layout reads better
  elsewhere), inside the article so hooked content is Pagefind-indexed.

The demo will override it to render its rating/date-read line (task filed
there) once this ships in a tagged module release.
