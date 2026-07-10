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
