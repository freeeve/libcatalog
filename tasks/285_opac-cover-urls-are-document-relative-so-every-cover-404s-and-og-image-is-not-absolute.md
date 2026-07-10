# 285 -- OPAC cover URLs are document-relative so every cover 404s and og:image is not absolute

Filed from libcat-e2e on 2026-07-10 (cross-repo ask).

`lcat export --covers-out` flattens `data/covers/<shard>/<file>` to `<out>/<file>` and
documents the result as *"the **site-relative** `covers/` URLs the editorial
`lcat:extra/cover` statements point at"* (`export/export.go:51-53`). The projector duly
emits `extra.cover = "covers/<workID>.jpg"`.

Every template then renders that string **verbatim**, into `src` and into `content`. A URL
with no leading slash is **document-relative** in HTML, not site-relative. So the cover on
a Work page resolves against `/works/<id>/`, and the same cover on the browse list
resolves against `/works/`. Neither is where the file is.

No cover has ever rendered from a `--covers-out` export. Not one.

## Symptom

libcat's own `hugo/exampleSite` sets `[params] covers = true` and ships a catalog with
**zero covers**, so the slot renders only lettered placeholders and nothing exercises the
image path. Adding one cover in the documented shape (`extra.cover =
"covers/wexampleone.jpg"`) and building the reference site with `hugo`, `baseURL =
https://example.org/`:

```
/works/wexampleone/   <img class="lcat-cover lcat-cover--detail" src="covers/wexampleone.jpg" …>
                        -> https://example.org/works/wexampleone/covers/wexampleone.jpg   404

/works/               <img class="lcat-cover lcat-cover--card"   src="covers/wexampleone.jpg" …>
                        -> https://example.org/works/covers/wexampleone.jpg               404

copyCovers writes it at  https://example.org/covers/wexampleone.jpg                       200
```

**The same string yields a different wrong URL on every page**, because the page's depth
decides it. That is the signature of a document-relative URL where a site-relative one was
meant.

Confirmed independently on the running playground OPAC (`:8482`), whose corpus carries
exactly one cover and whose site has `covers` **off** -- so the `<img>` is never emitted,
but the head is:

```
GET /works/w0cfnsjg6micju/
  <meta property="og:image"  content="covers/w0cfnsjg6micju.jpg">
  <meta name="twitter:image" content="covers/w0cfnsjg6micju.jpg">
  <meta name="twitter:card"  content="summary_large_image">
  "image":"covers/w0cfnsjg6micju.jpg"          (JSON-LD)

  /works/w0cfnsjg6micju/covers/w0cfnsjg6micju.jpg -> 404
  /covers/w0cfnsjg6micju.jpg                      -> 404
```

Three further consequences, all live in that transcript:

- **`og:image` must be an absolute URL.** The Open Graph protocol requires it; a scraper
  has no context to resolve against beyond the page URL, and resolving against the page URL
  gives the 404 above. The same applies to `twitter:image` and to JSON-LD `image`.

- **`twitter:card` is upgraded to `summary_large_image` on the strength of a cover that
  does not exist** (`head-seo.html:53`, `{{ if $image }}`). The card promises a large image
  and has none. Without a cover the site correctly emits `summary`.

- **`head-seo.html` never checks `site.Params.covers`.** The playground has covers *off*,
  renders no cover anywhere, and still advertises `og:image` for the one work that has one.
  A catalog that deliberately does not publish covers publishes them to Facebook.

## Root cause

`.Params.cover` is used raw at four sites, and `absURL`/`relURL` appears at exactly one
place in the whole partial -- on the *fallback*:

```
hugo/layouts/_partials/head-seo.html:43   {{- $image := .Params.cover | default "" -}}
hugo/layouts/_partials/head-seo.html:44   {{- if and (not $image) site.Params.ogImage }}{{ $image = (site.Params.ogImage | absURL) }}{{ end }}
hugo/layouts/_partials/head-seo.html:99   {{- with .Params.cover }}{{ $ld = merge $ld (dict "image" .) }}{{ end -}}
hugo/layouts/page.html:9                  partial "lcat-cover.html" (dict "url" .Params.cover …)
hugo/layouts/_partials/work-card.html:26  partial "lcat-cover.html" (dict "url" .Params.cover …)
hugo/layouts/_partials/lcat-cover.html:17 <img class="{{ $cls }}" src="{{ $url }}" …>
```

Line 44 is the tell. **The author knew a social image must be absolute, applied `absURL` to
the site-wide fallback, and did not apply it to the per-work cover one line above.** The
guard exists; it is next door.

`grep -rn 'absURL\|relURL' hugo/layouts/_partials/head-seo.html` returns line 44 and
nothing else.

### The two shapes, measured

Rendered from inside the reference site, so these are Hugo's answers, not mine:

| input | `absURL` | `relURL` |
|---|---|---|
| `covers/x.jpg` | `https://example.org/covers/x.jpg` | `/covers/x.jpg` |
| `/covers/x.jpg` | `https://example.org/covers/x.jpg` | `/covers/x.jpg` |
| `https://img1.od-cdn.com/a/b.JPG` | *(unchanged)* | *(unchanged)* |

`absURL` lands exactly on the file `copyCovers` wrote, and **passes an already-absolute URL
through untouched.** That last row is why this is one line per call site and not a
migration: queerbooks' covers are absolute OverDrive CDN URLs, which is the only reason its
covers render today and the only reason this has gone unnoticed in the one deployment that
turns covers on.

## Why it matters

**It is the entire cover feature.** tasks/025 built the slot; tasks/215 built
`--covers-out` to populate it. The URL shape those two agree on is the one shape no template
renders. An adopter who uploads covers through the admin and exports them gets a catalog of
broken images and lettered placeholders -- with no error, because a 404 image is a silent,
visual failure.

**Nothing in the repo can see it.**

- `hugo/exampleSite` turns covers on and has no cover to render. The image path has no
  fixture anywhere in the module.
- `link_check.cjs:44` is `const hrefRe = /href="([^"]+)"/g` -- it scans `href` and nothing
  else. An `<img src>` is invisible to it, so it passes clean on a build where every cover
  is broken. (This is the second time that file's guarantee has been narrower than its
  comment; see tasks/276.)
- `availability_test.cjs`, `negatives_test.cjs`, `sidebar_test.cjs` mock the reader and
  never render `lcat-cover.html`.
- `a11y_audit.js` would not flag it: `alt=""` is correct (WCAG H67 -- the adjacent title
  names the Work), so a broken *decorative* image raises nothing.

**The social-card half is worse than a missing image**, because it is the half a library
uses to promote a book. Every Work with a cover posts to Facebook, Slack and Mastodon as a
large-image card whose image 404s, and JSON-LD hands Google a `Book.image` that does not
resolve.

## Expected

- **`absURL` the cover wherever it enters an absolute context; `relURL` where it enters
  `src`.** Both are no-ops on the absolute URLs queerbooks ships, so no deployment
  regresses:

  - `head-seo.html:43` -- absolutize `$image` once, after the `default ""`.
  - `head-seo.html:99` -- `(dict "image" (. | absURL))`.
  - `lcat-cover.html:17` -- `src="{{ $url | relURL }}"`; both callers inherit it.

- **Gate `og:image` on `site.Params.covers`**, the way `page.html:9` gates the `<img>`. A
  site that does not publish covers should not advertise them. Today the playground does.

- **Do not upgrade `twitter:card` to `summary_large_image` for an image the page does not
  show.** Tie it to the same condition as the image itself.

- **Give the reference site a cover.** One work in `hugo/exampleSite/assets/catalog.json`
  with `extra.cover` renders the slot for the first time, and would have caught this the day
  tasks/215 landed. Add a second with an absolute URL so the pass-through stays covered.

- **Teach `link_check.cjs` about `src`.** One alternation in its regex makes it see images,
  and it already resolves paths against the built tree. It is the guard that should have
  caught this and structurally could not.

## Repro

```bash
cd ~/libcat-e2e && node harness/probe_opac_cover.mjs   # builds hugo/exampleSite with covers injected
cd ~/libcat-e2e && node harness/retest.mjs             # check t285
```

The probe copies `hugo/exampleSite` to a scratch directory, adds one `extra.cover` in the
documented site-relative shape and one absolute CDN cover, builds it with `hugo` against the
working tree's module, and asserts that no rendered cover URL is document-relative. It never
writes inside `~/libcat`. Its controls carry the argument: a work with **no** cover must
still render the lettered placeholder (so `covers = true` is live), and the **absolute** CDN
cover must pass through byte-for-byte (so a fix that blindly prepends `baseURL` fails here
rather than in production).

`t285` additionally reads the **live** playground OPAC on `:8482`, read-only, where the
relative `og:image` is observable without any build:

```bash
curl -s http://localhost:8482/works/w0cfnsjg6micju/ | grep -E 'og:image|twitter:(image|card)'
curl -s -o /dev/null -w '%{http_code}\n' http://localhost:8482/covers/w0cfnsjg6micju.jpg
```
