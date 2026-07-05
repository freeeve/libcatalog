# 118 -- Header nav hook + banner slot, so adopters can stop shadowing baseof.html

Filed from libcatalog-demo. The 020 hooks (head-extra.html, footer.html, hero block)
removed most reasons to shadow the module base -- but the demo still carries a full
`layouts/baseof.html` shadow for exactly one reason: the HEADER has no extension
point. Its shadow adds (see libcatalog-demo `layouts/baseof.html`, the EVL+ lines):

- a primary nav rendered from Hugo's `site.Menus.main` (aria-current on the active
  section, section-landing detection for child pages),
- a brand-mark slot inside the `.lcat-brand` link (logo/colophon next to the title),
- a site-wide banner ABOVE the header (the demo's "this is a demo" ribbon; real
  libraries would use it for closure notices / emergency messages).

Shadowing baseof means re-diffing after every module release -- the one maintenance
trap left for adopters. Proposal, all empty-by-default like footer.html:

1. `{{ partial "banner.html" . }}` first thing in <body> (after the skip link).
2. Render `site.Menus.main` in the header when the site defines it (a `nav.html`
   partial with the module's own accessible markup; adopters get menus via config
   alone) -- or at minimum an empty `header-extra.html` hook between brand and search.
3. A `brand.html` partial wrapping the current brand anchor, so a logo can be
   shadowed in without touching the base.

With those, the demo can delete its baseof shadow entirely (it will -- happy to be
the guinea pig adopter). The demo's nav partial (`layouts/_partials/nav.html`) is
i18n'd and axe-clean; lift whatever is useful.

## Status (2026-07-05 session)

Done. `banner.html` (empty hook, first in <body> after the skip link),
`brand.html` (the .lcat-brand anchor's content, default site title), and a
module-owned `nav.html` that renders `site.Menus.main` when defined -- markup
lifted from the demo's partial (aria-current, section-landing detection,
`primaryNav` i18n label) with a `.lcat-nav` style block on the module tokens.
The exampleSite exercises it bilingually (en menu + [languages.es.menus.main]
override); axe (95 pages) and link audits clean. The demo can now drop its
baseof.html shadow: banner.html <- demo-banner, brand.html <- brand-mark +
name span, [[menu.main] config <- its nav partial (or shadow nav.html to keep
its exact markup), and tasks/119's default SEO head replaces its head/seo
override.
