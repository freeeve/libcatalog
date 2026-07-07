# 160 -- Opt-in static generation of curated views for SEO

Plane 2 opt-in of [154]. A deployment may pin specific views -- e.g. curated
lists -- to hard HTML for SEO, beyond the default single-combination views
(details + browse shell, task 157). **Opt-in only; never every combination by
default.**

## Rationale

Editorially important collections (curated lists, a themed subject page) benefit
from a crawlable, hard-HTML URL. But pre-rendering *all* combinations is the
explosion task 157 removed. So: keep the default minimal, and let the operator
name the handful of views worth freezing.

## Scope

- A deployment-config list of views to render statically -- e.g. curated-list
  slugs, or specific named facet/subject queries -- each producing a hard HTML
  page included in the sitemap.
- The client-side app (task 158) still serves those same views interactively;
  the static page is an SEO/first-paint mirror, not a replacement.
- These pinned views regenerate on the incremental path (task 159) when their
  inputs change (e.g. a list's membership, or a matching work).

## Design notes

- Reuse the per-facet rendering capability task 157 preserves-behind-the-flag;
  this task is the opt-in surface that selects which ones to emit.
- Curated lists are editorial data (see the site-data overlays in the reference
  deployments); the config points at those, it does not re-derive them.

## Out of scope

- Auto-selecting "popular" views to freeze -- explicit opt-in only for now.

## Verify

- With no opt-in config, the build emits only the task-157 default set.
- With a curated list pinned, that list renders to static HTML, appears in the
  sitemap, and still works client-side.
- Editing the list's membership regenerates its static page on the incremental
  path.
