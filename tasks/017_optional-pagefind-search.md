# 017 -- Optional Pagefind search (default out-of-the-box; roaringrange as advanced)

## Context

Search has two possible engines now, and they sit at very different complexity points:

- **roaringrange** (built: `tasks/005`/`010`). The Go build-side emits per-language
  term (`.rrt`) + BM25 (`.rrb`) indexes and a trigram (`.rrs`) arm for CJK. But the
  **browser reader (`tasks/009`) is unbuilt** and needs bespoke WASM-reader vendoring +
  a query UI. High control, high complexity. Designed for scale (split-set sharding).
- **interim** `hugo/assets/lcat-search.js` -- a client-side substring filter over the
  rendered list. Ships today as progressive enhancement; not real ranked search.
- **Pagefind** (https://pagefind.app) -- a static search library that indexes the
  **final built HTML** (`public/`), not markdown. Confirmed relevant facts:
  - Works on rendered HTML, so the module's **content-adapter-minted pages index fine**
    (no markdown needed -- Pagefind runs post-`hugo` over `public/`).
  - **Per-language**: detects `<html lang>` and builds a separate index per language,
    auto-loading the right one. This dovetails with `tasks/016`, which just made
    `<html lang>` correct per language.
  - **CJK segmentation + language-adaptive stemming** built in (extended release, the
    `npx pagefind` default) -- i.e. it delivers natively much of what `tasks/005` built
    by hand in roaringrange.
  - Drop-in **Component UI** (Pagefind >= 1.5), `data-pagefind-filter` for facets,
    `data-pagefind-body`/`-meta`/`-ignore` for scoping. Ships its own tiny WASM behind
    the CLI -- **no custom WASM wiring**.
  - Run via `npx pagefind --site public` (Node) or the standalone binary.

## Decision (choose-your-search)

**Pagefind is the default, out-of-the-box search; roaringrange is the opt-in advanced
path.** Rationale: Tier 1's whole pitch is static, self-serve, no backend, minimal
complexity (ARCHITECTURE §6). Pagefind resolves the real current gap (no browser reader,
weak substring filter) with far less bespoke code, gets multilingual + CJK **for free**
off the `<html lang>` we already emit, and is a well-maintained ecosystem standard.
roaringrange stays for deployments that need scale (very large corpora, split-set
sharding), custom ranking, or a **no-Node** build -- it is not wasted, just repositioned
as advanced; `tasks/009` becomes the advanced-path option rather than the only path.

## Scope

1. **Template markup.** Annotate the Hugo templates for Pagefind: `data-pagefind-body`
   on the Work detail main content; `data-pagefind-meta` for title/author; and
   `data-pagefind-filter` mapped to the existing facet dimensions (format, language,
   subject, tag, contributor, classification) so Pagefind filtering reuses the facets;
   `data-pagefind-ignore` on chrome/nav/sidebar so only Work content is indexed.
2. **Search UI, opt-in.** A site param (e.g. `[params.search] engine = "pagefind"`)
   swaps the search partial to the Pagefind Component UI; default/absent keeps the
   interim `lcat-search.js` filter. Progressive enhancement: no JS -> the form still
   submits to `/works/`.
3. **Build wiring.** Document the post-build step (`npx pagefind --site public` or the
   standalone binary) in the README + a make/npm script; keep it out of Hugo itself.
   Pagefind output lands in `public/pagefind/` (gitignored, like `public/`).
4. **Multilingual + CJK check.** Verify Pagefind builds per-language indexes off the
   bilingual exampleSite (`tasks/016`) and segments a CJK sample -- the acceptance that
   `tasks/005` targeted, delivered via Pagefind here.
5. **Docs.** README "Search" section (Pagefind default path + roaringrange advanced
   path + the no-JS fallback); update ARCHITECTURE §8 to present both engines and the
   default; note `tasks/009` is now the advanced-path reader, not the sole plan.

## Tradeoffs (document, don't hide)

- Adds an **optional Node/npx (or standalone-binary) post-build step** at deploy. It is
  post-build, optional tooling -- same category as the a11y audit already shipped -- and
  does not pull into the Go core or the Hugo module runtime.
- Less control over ranking internals than roaringrange's BM25; fine for Tier 1.
- Some overlap with `tasks/005`/`010`: Pagefind natively covers the multilingual/CJK
  goal those chased. Those stay valid for the advanced/scale path; the browser-reader
  half (`009`) is de-prioritized, not deleted.

## Acceptance

- With Pagefind enabled, a built site has working full-text ranked search over Work
  pages -- multilingual (en + es), CJK-capable, with facet filters -- and **no bespoke
  WASM wiring**.
- Pagefind is opt-in via a site param; with it off, the interim filter still works and
  the no-JS path still browses.
- README + ARCHITECTURE present the choose-your-search decision (Pagefind default,
  roaringrange advanced).

## Refs

- https://pagefind.app (multilingual, filters, Component UI, CLI).
- `hugo/layouts/_partials/search.html`, `hugo/assets/lcat-search.js` (interim filter),
  `hugo/layouts/page.html` (Work detail body to scope with `data-pagefind-body`).
- ARCHITECTURE §8 (search); `tasks/005` (per-language/CJK, now native in Pagefind),
  `tasks/009` (roaringrange browser reader -> advanced path), `tasks/010` (roaringrange
  build-side indexes), `tasks/016` (`<html lang>` per language -> Pagefind multilingual).
