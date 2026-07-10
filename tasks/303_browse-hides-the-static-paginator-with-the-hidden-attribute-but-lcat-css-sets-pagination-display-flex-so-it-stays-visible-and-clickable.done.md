# 303 -- browse hides the static paginator with the hidden attribute, but lcat.css sets .pagination display:flex so it stays visible and clickable

Filed from queerbooks-demo on 2026-07-10 (cross-repo ask).

Adopting v0.122.0. **281's substance works** -- see the numbers at the bottom. This
is the one part of it that does not take effect, and it is the part that keeps a
reader from losing their facets.

## What happens

`setPagerHidden(true)` runs, the attribute lands, and the paginator stays on
screen:

    // after a facet click, on the home page
    { tag: "UL", hasHiddenAttr: true, closestNav: false,
      computedDisplay: "flex", offsetHeight: 44 }

`hidden` only hides via the UA rule `[hidden] { display: none }`. Any author
`display` declaration on the same element outranks it -- and lcat.css has one, 26
lines away from the problem:

    lcat.css:506   .pagination { list-style: none; display: flex; flex-wrap: wrap; … }

Hugo's `_internal/pagination.html` emits a bare `<ul class="pagination
pagination-default">` with **no `<nav>` wrapper**, so `el.closest("nav") || el`
resolves to the `<ul>` itself -- the very element `.pagination` styles. The
fallback path is the only path here, and it is the one the CSS defeats.

So the reader still sees "Next", and clicking it still navigates to
`/page/2/`, silently discarding the query and the facets. That is precisely the
behaviour the code comment says it is preventing:

    // While browse owns the list it is a control that silently discards the
    // reader's query and facets, so hide it (tasks/281).

## Fix

One rule, next to the existing one:

    .pagination[hidden] { display: none; }

Or hide the element the way the CSS can't argue with (`el.style.display = "none"`
/ restore to `""`), or wrap the paginator in a `<nav>` in a template you control
so `closest("nav")` finds an unstyled ancestor. The CSS rule is the smallest and
keeps the behaviour declarative.

Worth a regression test that asserts on `getComputedStyle(el).display` or
`offsetHeight`, not on `hasAttribute("hidden")` -- the attribute was set the whole
time, so an attribute-level assertion would have gone green.

## Everything else in 281 checks out

On our 62,602-work catalog, both locales, after a facet click:

    count text (en)   "showing the first 60 of 21792 results"
    count text (es)   "mostrando los primeros 60 de 21792 resultados"
    cards rendered    60
    "N+" suffix       gone
    total             exact (21,792), and the click delivers what the rail promised

The `data-lcat-*` attributes localize correctly; our es.toml supplies all three
and the English fallbacks never appear. The whole-match-set search is the right
call -- the rail no longer advertises a number the click cannot deliver.

## Outcome

Fixed in **v0.124.1** (patch), commit `f3828cc`. Diagnosis was exactly right,
down to the `closest("nav") || el` resolving to the styled element.

Reproduced first, in headless Chromium against the exampleSite with
`[pagination] pagerSize = 2`, before changing anything:

    cold:              hidden=false display=flex offsetHeight=44
    after facet click: hidden=true  display=flex offsetHeight=44   <- the bug

### The fix is one rule, but not the one suggested

`.pagination[hidden] { display: none; }` would have worked. It would also have been
the **third** per-component copy of that rule: `grep` finds

    lcat.css:402  .lcat-excluded[hidden] { display: none; }
    lcat.css:559  .lcat-theme-toggle[hidden] { display: none; }

Two components had already hit this and been patched one at a time. The paginator
was the third and got missed, which is what a per-component fix predicts. So:

    [hidden] { display: none !important; }

stated once, near the top, and both per-component copies deleted. `!important` is
the point rather than a shortcut -- the entire failure mode is a later, more
specific author rule outranking the UA `[hidden]` rule.

Verified the deletions are covered rather than merely tidy: `.lcat-excluded[hidden]`
still computes `display:none` with its own rule gone, and forcing `hidden` onto
`.lcat-theme-toggle` in the live page moves it from `block/27px` to `none/0px`.

Audited the other seven `el.hidden = ...` sites in `assets/*.js`. `.lcat-facet-not`
and `.lcat-facets ul` set no `display`, so the UA rule was already working there;
the paginator really was the only broken one.

### The test that should have caught it

`browse-scope.spec.mjs`'s `pagerVisible()` read `!nav.hidden` -- the attribute,
which was set the whole time. It now reads `offsetHeight` and computed display, and
prints both in the check label.

Mutation matrix, both run for real:

- Reset removed, new assertion: **1 failed**, and the label names the cause --
  `static pager is off screen ... [hidden=true display=flex h=44]`. The cold-pager
  control stays green, so it is not failing vacuously.
- Reset removed, **old** assertion restored: **exit 0, all green**, with its own
  label printing `display=flex h=44`. That is the blind spot, demonstrated rather
  than asserted.

### Gates

65 jsdom checks, 61 Playwright checks across 4 specs, axe over 124 pages, link
check. Release needed a one-path `git stash` around a concurrent session's
uncommitted `tasks/304`; restored byte-identical (sha `bef4e615…` before and after).
