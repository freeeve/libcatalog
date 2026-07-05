# 119 -- Default SEO head: meta description, canonical, Open Graph/Twitter, JSON-LD

Filed from libcatalog-demo. The module's <head> is a bare <title> plus the
head-extra hook; every adopter has to hand-roll the rest. The demo's
`layouts/_partials/head/seo.html` (which is why it overrides the module <title>
via its baseof shadow) has carried this since its tasks/005 SEO pass:

- <meta name="description"> from page/params/site fallbacks,
- canonical URL,
- og:title/description/type/url/image (per-work cover image when present, site
  fallback image otherwise) + twitter:card,
- JSON-LD: schema.org `Book` on Work pages (title, author, ISBNs, subjects),
  `Library`/`WebSite` on the homepage.

The Book JSON-LD is the piece only the module can do properly -- it owns the Work
page params (contributors, identifiers, subjects), and every catalog adopter
benefits from works being indexable as books. Proposal: render sensible defaults in
the module base (description, canonical, OG, Book JSON-LD on Work pages), each
suppressible/overridable via a params switch or by shadowing one small partial --
keep head-extra.html for additions. Demo's partial is a working reference
implementation to lift from.

## Status (2026-07-05 session)

Done. New module partial `layouts/_partials/head-seo.html`, called from the
base <head> in place of the bare <title>: meta description (page > params >
synthesized "Title by Authors · Site" via the localizable
`workMetaDescription` i18n key > site default), canonical + hreflang
alternates, Open Graph/Twitter (work cover -> og:image, [params] ogImage
fallback, og:type book on works), and JSON-LD -- WebSite + SearchAction into
/works/ on the homepage, Book on Work pages built from the adapter-owned
params (role-filtered authors, per-edition workExample with bookFormat +
ISBN, localized subject labels as `about`, tagLabels as genre, inLanguage
from the MARC code). `[params.seo] disable = true` suppresses everything but
<title>; shadowing the one partial gives finer control; head-extra.html
stays the additions hook (icons/manifest/theme-color deliberately left to
it -- they are site asset files the module cannot ship). Verified on the
built exampleSite: en and es pages carry localized descriptions ("de" vs
"by"), correct canonicals, both JSON-LD shapes parse. Icons block from the
demo's partial was NOT lifted (site-specific); the demo keeps those in
head-extra and can delete the rest of its seo override plus the <title>
reason for its baseof shadow.
