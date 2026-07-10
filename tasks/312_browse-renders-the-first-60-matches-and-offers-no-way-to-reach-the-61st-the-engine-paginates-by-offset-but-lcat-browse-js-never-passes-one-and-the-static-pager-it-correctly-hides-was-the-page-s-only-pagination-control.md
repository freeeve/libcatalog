# 312 -- browse renders the first 60 matches and offers no way to reach the 61st: the engine paginates by offset but lcat-browse.js never passes one, and the static pager it correctly hides was the page's only pagination control

Filed from libcat-e2e on 2026-07-10 (cross-repo ask).

This is the task tasks/281's Outcome asks for by name:

> Browse still has no pagination. Past the first 60 the reader is told the total and given no
> way to reach the rest. `search(q, offset, PAGE, ...)` supports it; it wants a task of its own.

And which `lcat-browse.js:862-863` describes:

> Browse has no pager of its own yet; the count says how many are held back, which is honest,
> where "Next" was not.

Filing it with measurements, because the numbers are worse than "no pager" suggests.

## Symptom

Measured read-only against the published queerbooks OPAC on `:8502`, driving the real WASM
reader in a real browser.

```
corpus                                62602 works, 2609 static pages of 24
search "lesbian"                       9923 matches
browse renders                           60 cards          <- 99.4% of the result set discarded
visible pager links, query active         0
next / more / load-more controls          0
scrolling to the bottom              60 -> 60 cards
```

The same ceiling on the facet path, and on the path a reader takes **without typing anything**:

```
"lesbian" + facet "LGBTQ+ people"      8307 matches, 60 cards
no query, facet "LGBTQ+ people" only  21792 matches, 60 cards, 0 pager links
```

`refresh()` restores the static list only when the query **and** the filters are empty. So
clicking one subject -- the primary discovery path on a public catalog, where the reader never
types -- hands `allIds` to `filterIds` and then slices at `PAGE`. **21,792 works about LGBTQ+
people; 60 of them reachable.**

### The engine already paginates

This is not an engine limitation. `search(q, offset, len, ...)` takes an offset, and it works:

```
search("lesbian", 0,  60, 0, [])   -> 60 ids
search("lesbian", 60, 60, 0, [])   -> 60 ids, 0 of them shared with the first call,
                                       and exactly ids[60..120] of the 9923-id full set
```

The UI never passes an offset. `grep -n 'offset\|loadMore\|scroll\|IntersectionObserver\|nextPage'
hugo/assets/lcat-browse.js` returns nothing.

## Root cause

`hugo/assets/lcat-browse.js`. Since tasks/281 the browse path computes the **whole** ranked match
set and then throws almost all of it away at render time:

```js
catalog.search(q, 0, allIds.length, 0, [])      // every hit (tasks/281's fix)
…
records.getMany(base.ids.slice(0, PAGE))        // :917  query path
records.getMany(ids.slice(0, PAGE))             // :927  facet path
```

`base.ids` holds all 9923 ids in memory. `.slice(0, PAGE)` drops 9863 of them. `PAGE` is 60, and
its own comment (`:54-55`) already flags the confusion:

```js
// Three distinct numbers, one of which used to be all three (tasks/281).
const PAGE = 60; // result cards rendered; the reader's search page is NOT this
```

There is no reader's search page. `PAGE` is the only number, and it is a render cap.

The one pagination control on the page is Hugo's static pager, which walks the **unfiltered**
corpus. Browse hides it while it owns the list (`setPagerHidden(true)`, tasks/281 + tasks/303) --
correctly, because clicking it silently discarded the query and the facets. So the reader is left
with no control at all.

**Un-hiding it would not help.** Measured: searching from `/works/page/2/` renders the same first
60 cards as searching from `/works/`. The static pager pages the server-rendered corpus; browse's
result set has no relationship to `/page/N/`.

## Why it matters

**Every discovery path on the public catalog dead-ends at 60.** Search dead-ends at 60. Clicking
a subject dead-ends at 60. Only the unfiltered a-to-z list paginates, and it paginates 2609 pages
deep -- so the OPAC's own behaviour tells the reader that pagination exists, right up until they
express an interest in something.

**The count now says exactly how many works the reader cannot see.** tasks/281's fix replaced a
misleading `"60+"` with an honest `"showing the first 60 of 9923 results"`. That was the right
fix, and it converted a silent truncation into a visible dead end. The dead end is what is left.

**It is worst for the queries that matter most.** A patron searching a broad subject on a queer
catalog -- `lesbian` (9923), `LGBTQ+ people` (21792) -- gets the 60 the ranker happened to like.
A patron searching a rare title gets everything. The catalog is complete for the questions that
did not need a catalog.

**The data is already client-side.** `base.ids` is a `Uint32Array` of every match, and
`records.getMany` fetches records by id over HTTP Range. Rendering ids 60..120 costs one more
`getMany` call over bytes the browser is already able to fetch. Nothing needs to be re-searched,
re-ranked, or re-downloaded.

## Expected

- **Give browse a pager of its own.** Keep the whole match set (it is already in hand), track an
  offset, and render `ids.slice(offset, offset + PAGE)`. The engine's `offset` argument is not
  even needed for the query path, since `base.ids` is the full set -- slice it. It *is* needed if
  you would rather not hold every id, but holding them is what tasks/281's fix already does.

- **Separate the three numbers `PAGE` is currently doing duty for.** The comment at `:54` says
  *"Three distinct numbers, one of which used to be all three."* There is now a fourth: the
  reader's page size. `PAGE = 60` as a render cap and `PAGE` as a page length want different
  names, or the next reader will fix pagination and re-break facet counts.

- **Match the static pager's affordance.** The cold list already renders `ul.pagination` with
  Prev/Next/numbered links, and `setPagerHidden` already knows how to hide and restore it.
  Rewriting its `href`s and letting browse own it would put pagination exactly where the reader
  already looks for it, rather than inventing a second control.

- **Or, minimally, a `Load more` button.** 60 → 120 → 180. It is one `getMany` per press and it
  removes the dead end. `"showing the first 60 of 9923"` becomes `"showing 120 of 9923"`.

- **Consider the URL.** tasks/219 established that discovery state never reaches the URL in the
  admin SPA. Browse has the same property: a query, a facet and a page are all invisible to the
  address bar, so no filtered result set can be linked, bookmarked, or shared. A pager makes that
  more visible, not less. Out of scope here, but the same fix touches it.

## Repro

```bash
cd ~/libcat-e2e && node harness/probe_opac_browse_pagination.mjs   # N4-N8
cd ~/libcat-e2e && node harness/retest.mjs                         # check t312
```

**READ-ONLY against the published OPAC on `:8502`.** No credentials, no writes, no mutations.
Touches neither `:8481` nor `:8501`.

Its controls carry the argument. **`N1` shows the unfiltered list paginates 2609 pages deep**, so
pagination is missing from *search*, not from the OPAC -- that contrast is the finding. `N2` shows
the count line is honest, so this is a dead end and not a silent truncation. **`N3` drives the
engine's offset directly** -- `search(q, 60, 60, …)` returns 60 ids disjoint from `search(q, 0,
60, …)` and equal to `ids[60..120]` of the full match set -- so the capability exists today and
only the UI declines to use it. `N0` confirms the WASM reader actually range-fetched, so the
numbers come from browse and not from a static list.

An earlier version of this probe reported a bug that does not exist: that the facet rail promised
**8055** for "Lesbians" and delivered **21792**. Hugo server-renders the facet panel sorted by
count, and browse **replaces that panel on boot with its own, in its own order**. The probe read
the label and count from `rows.nth(0)` of Hugo's panel, then clicked `rows.nth(0)` of browse's --
a different row. The rail keeps its promise exactly: promised 21792, delivered 21792, and
`filterIds(allIds, [["subject", homoit0000915]])` says 21792. The probe now reads the row that
ends up **checked**, never the row that was there before the click.
