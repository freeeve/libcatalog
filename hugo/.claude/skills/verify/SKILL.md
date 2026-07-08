---
name: verify
description: Build, serve, and drive the libcat Hugo module's exampleSite to verify template/asset changes at the rendered-site surface.
---

# Verifying hugo-module changes

The surface is the **built site in a browser**, not the templates. Recipe:

1. **Build** (fast, <5s): `cd hugo/exampleSite && hugo --quiet --destination <scratch>/public`.
   To test an opt-in param without touching the repo, overlay a config:
   `hugo --config hugo.toml,<scratch>/overlay.toml --destination ...`
   (later files win; absolute overlay paths work).
2. **Regression guard**: build once from a clean tree first, again after the
   change, `diff -r` the two outputs. Template refactors that shouldn't change
   defaults must diff empty (modulo fingerprint renames from edited assets).
3. **Serve + drive**: `python3 -m http.server <port> -d <scratch>/public
   --bind 127.0.0.1`, then drive pages with jsdom
   (`hugo/node_modules/jsdom`): `JSDOM.fromURL(url, { resources: new
   ResourceLoader(), runScripts: "dangerously", pretendToBeVisual: true })`
   executes the site's real script assets. jsdom v24 has **no window.fetch**
   -- bridge it in `beforeParse(w) { w.fetch = (u,o) => fetch(new URL(u,
   url).href, o) }` for any fetch-using asset (lcat-sidebar.js,
   lcat-availability.js). Settle ~1.5s before asserting on hydrated DOM.
4. **Static gates** (from `hugo/`): `npm run test:js` (jsdom unit tests),
   `node a11y_audit.js <built-dir>` (axe, exits non-zero on violations),
   `node link_check.cjs <built-dir>`.

Gotchas:
- `jsdomError: Could not load ... /pagefind/pagefind-ui.js` is expected noise:
  the pagefind index is a post-build step (`npm run search:index`).
- Fragment assets under `/lcat/` are not documents; a11y_audit skips them.
- The exampleSite is bilingual -- check `/es/...` pages when a change touches
  anything per-language (partialCached caches, i18n, fragment assets).
