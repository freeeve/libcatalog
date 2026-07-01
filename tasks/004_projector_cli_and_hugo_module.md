# 004 — Projector CLI + Hugo module (Tier 1)

Framework core. `docs/ARCHITECTURE.md` §7; `docs/ROADMAP.md` Phase 2. The static
tier: render a catalog from the BIBFRAME graph as a drop-in component of a
library's Hugo site, with no per-record markdown.

## Goal

Two distributable artifacts: a projector CLI and a Hugo module.

## Approach

1. **Projector CLI** (`cmd/lcat`, `project/`): `BIBFRAME graph → catalog data
   (JSON) + search index`. Also the import/export front door
   (MARC/MODS/BIBFRAME).
2. **Hugo module** (`hugo/`): catalog layouts, partials (facets, vocabulary
   picker, live-availability + search JS assets), and a **content adapter**
   (`_content.gotmpl`, Hugo ≥ 0.126) that mints a Page per Work from the
   projected JSON — no content files.
   - Port qllpoc's templates/partials/assets as the starting point: Work/detail,
     browse/list, series, creator, subject-hierarchy; the `searchsource` /
     `creators` custom outputs become projector outputs.
- Theming: consumers layer their own Hugo theme/overrides on top of the module.

## Acceptance

- A vanilla Hugo site with only the module + a MARC/BIBFRAME dump builds a
  faceted catalog (Tier-1 smoke test), zero content files.
- Pagefind indexes the emitted HTML (module carries `data-pagefind` markers).

## Refs

- Hugo content adapters (`_content.gotmpl`, Hugo ≥ 0.126).
- Extraction reference: qllpoc `site/layouts/**`, `site/assets/**`,
  `site/hugo.toml`.
