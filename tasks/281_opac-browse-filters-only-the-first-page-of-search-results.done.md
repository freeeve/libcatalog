# 281 -- OPAC browse filters only the first page of search results

Filed from libcat-e2e on 2026-07-09 (cross-repo ask).

`lcat-browse.js` builds its ranked base set with `catalog.search(q, 0, PAGE, 0, [])`,
where `PAGE = 60`. Per `roaringrange.js:518-522`, the third argument is a page length:
*"The page covers ranked doc IDs `[offset, offset+len)`."* So the base set is **the
first sixty ranked hits**, and every facet the reader then picks is intersected with
those sixty rather than with the result set.

A patron on queerbooks who searches **lesbian** and clicks the facet **LGBTQ+ people**
is shown **51 results**. The true answer, from the same WASM reader on the same
artifacts, is **8307**.

The rail had already told her the right answer. With the query alone on screen, that
facet row reads **8307** -- `search()` computes `facetCounts` over the whole match set
regardless of the page length it was asked for. She clicks the number 8307 and gets 51.
The rail then **recomputes itself from the truncated base and rewrites 8307 to 51**, so
the number she clicked is gone before she can look at it again.

This directly falsifies the module's own headline promise (its header, tasks/177):
*"the rail never promises a result set it will not deliver."* It promises 8307 and
delivers 51.

Measured read-only against :8502 (queerbooks, 62,602 works). :8482 ships no `browse-*`
artifacts, so browse is only reachable on the queerbooks build.

## Symptom

Driven through the real page, then re-derived from the reader itself:

```
corpus                                              62602

query "lesbian" alone
  UI  .lcat-resultcount                             "60+ results"      60 cards
  cat.search("lesbian", 0, 60,     0, [])   -> ids     60     <- the UI's base
  cat.search("lesbian", 0, 100000, 0, [])   -> ids   9923     <- what matches

facet "LGBTQ+ people" (subject homoit0000915) alone
  UI  .lcat-resultcount                             "21792+ results"
  fac.filterIds(allIds, F)                  -> ids  21792     <- correct

query "lesbian" + facet "LGBTQ+ people"
  UI  .lcat-resultcount                             "51 results"       51 cards
  fac.filterIds(top60, F)                   -> ids     51     <- exactly what the UI shows
  fac.filterIds(full,  F)                   -> ids   8307     <- the truth
  cat.search("lesbian", 0, 100000, 0, F)    -> ids   8307     <- the truth, one call
```

`|top60 ∩ F| = 51` reproduces the UI's number exactly, which is the mechanism proved
rather than inferred. The reader is asked for a page and handed a page; the module
treats that page as the corpus.

**51 of 8307 is 0.6% of the answer.** The undercount is not a rounding error, and it is
not bounded: with a query active, **no facet selection can ever return more than 60
results**, because the base set has 60 members.

`facet alone` is correct, and that is the tell. Its base is `allIds` -- every doc id --
because there is no query. Only the query path truncates.

### The rail promises 8307, delivers 51, then erases the 8307

Same two selections, applied in the two possible orders. `rail` is the count rendered on
the `LGBTQ+ people` row; `count` is `.lcat-resultcount`:

```
O1  type "lesbian"          rail = 8307     count = "60+ results"     <- rail is CORRECT
    then click the facet    rail =   51     count = "51 results"      <- delivered 0.6% of it

O2  click the facet         rail = 21792    count = "21792+ results"  <- rail is CORRECT
    then type "lesbian"     rail =   51     count = "51 results"
```

O1 is the ordinary order -- search, then narrow -- and in it the rail is *not*
self-consistent: it advertises 8307 and yields 51. The engine is right both times. The
module renders the engine's true counts (`base.counts = res.facetCounts`) while rendering
a 60-id base beside them, then, on the click, recomputes the rail from those 60 ids.

**The rewrite is why this survived.** The moment the reader acts on the promise, the
promise is overwritten with the delivery, and the two agree. There is no state in which
8307 and 51 are on screen together. A reader sees a plausible number, clicks, and sees a
different plausible number; only a reader who *remembers* 8307 has any evidence, and
nothing in the DOM retains it.

## Root cause

`hugo/assets/lcat-browse.js:858-884`:

```js
const baseP = q
  ? catalog.search(q, 0, PAGE, 0, []).then((res) => ({   // <- PAGE is a page length
      ids: res.ids || new Uint32Array(0),
      records: res.records,
      counts: res.facetCounts,
    }))
  : Promise.resolve({ ids: allIds, records: null, counts: null });
return baseP.then((base) => {
  if (!filters.length) {
    renderCards(base.records || [], base.ids.length);
    setLiveCounts(countsToMap(base.counts), new Map(), base.ids);
    return;
  }
  return facets.filterIds(base.ids, filters, true).then((fi) => {   // <- 60 ids in
    const ids = fi.ids;
    ...
    records.getMany(ids.slice(0, PAGE)).then((recs) => renderCards(recs, ids.length));
```

with `PAGE = 60` at `:50`, and the reader's contract at `roaringrange.js:518-527`:

> *"The page covers ranked doc IDs `[offset, offset+len)`; `max_missing` is the fuzzy
> tolerance (0 = strict). ... `filters` ... an empty array `[]` means no filter ...
> Within a field categories OR, across fields they AND."*

**`search` already takes the filters.** The module passes `[]` and re-implements the
intersection client-side against a page. Calling `catalog.search(q, 0, PAGE, 0, filters)`
would return the correctly filtered page *and* `facetCounts` for the true set -- and
`cat.search("lesbian", 0, 100000, 0, F)` returns 8307, confirming the engine does this
correctly when asked.

**`PAGE` is doing three jobs.** At `:860` it is the search result-set size, at `:879`
the render slice, and at `:833` the threshold for a `"+"` suffix. Only the second of
those is a page size. The first should be the full ranked set (or a large cap); the
third is a display concern.

The module's own header describes the intended shape and does not mention a cap:

> *"One read shape over one shared doc space (tasks/177, the POC's `browse()`): a ranked
> base set -- `RrsCatalog.search(q, ..., [])` for a query, else every doc id -- then
> `RrfFacets.filterIds(base, filters)` + `records.getMany` for the survivors."*

*"else every doc id"* is exactly the asymmetry: the no-query base is the whole corpus,
the query base is one page. The sentence reads as though both are complete sets.

**The scope mismatch is stated, in a comment, at the point it is made.** `:861-864`:

```js
if (!filters.length) {
  // Query only: the search call already carries the page's records
  // and the query-filtered counts.
  renderCards(base.records || [], base.ids.length);
  setLiveCounts(countsToMap(base.counts), new Map(), base.ids);
```

*"the page's records"* and *"the query-filtered counts"* -- both true, and they are not
the same set. `res.facetCounts` is computed over every hit, not over the returned page.
Verified against the reader: `search("lesbian", 0, 60, 0, [])` and
`search("lesbian", 0, 100000, 0, [])` return an **identical** count of `8307` for
`subject=homoit0000915`, while their `ids` are 60 and 9923. `len` bounds `ids` and
`records`; it never bounds `facetCounts`.

So the rail is seeded with a correct number and `base.ids` is seeded with a page, and the
click intersects the facet with the page. That is the whole bug: **`facetCounts` and `ids`
answer different questions, and the module reads them as if they answered the same one.**

**The live-count promise is broken, then covered up.** From the same header (tasks/177):

> *"while a query or filter is active, every rendered count re-derives from the result
> set -- each category's postings intersected with the surviving ids -- so the rail never
> promises a result set it will not deliver."*

In the query-only branch the counts do **not** re-derive from `base.ids` -- they come
straight from the engine, over the full match set. The rail promises 8307. Then the click
enters the `filters.length` branch, where `cmap = countsToMap(fi.facetCounts())` *does*
re-derive from the 60 survivors, and the rail is rewritten to 51. The invariant is
violated in the first step and restored in the second, which is exactly the sequence that
destroys the evidence.

A probe that only checks "does the rail's number match the result count" reads whatever
was rendered *after* the click, and passes. I wrote that probe first (`B4`); it passed.
It only fails once it captures the rail **before** the click.

## Secondary: the only "more results" control on the page discards the search

`layouts/list.html:10` renders Hugo's static paginator. `lcat-browse.js` replaces
`#lcat-results` and `.lcat-resultcount` and **leaves the pager alone**. Measured on
`/works/` with `lesbian` typed and `"60+ results"` on screen:

```
pager links visible: 7
their hrefs:         /works/page/2/, /works/page/3/, /works/page/4/
```

Those are pages of the **server-rendered, unfiltered** list. A reader who has 60 results
and wants the next 60 clicks *Next*, lands on works 61-120 of the whole 62,602-work
corpus, and has silently lost both the query and the facets. It is the only control on
the page that offers more results, it is visible the entire time, and it is a trap.

There is no pagination inside browse mode: `grep -n offset assets/lcat-browse.js` returns
nothing, though `search(query, offset, len, ...)` takes one. Whatever is done about the
base set, the pager must either drive browse or be hidden while browse owns the list.

## Secondary: the `+` suffix means two different things

`:833`:

```js
countEl.textContent = total + (total >= PAGE ? "+ " : " ") + labels.results;
```

`total` is `ids.length` at both call sites -- never a page count. So:

- **Query path**: `ids` is capped at 60, the true total is unknown, and `"60+ results"`
  is honest and useful.
- **Filter path**: `ids` is the complete filtered set. `"21792+ results"` claims *at
  least* 21792 when the answer is exactly 21792.

Same glyph, opposite meanings, decided by a constant that has nothing to do with either.
Once 281's base set is correct, the query total is exact too and the `+` should go
entirely -- replaced, if anything, by "showing the first 60 of N".

## Why it matters

**It is the catalog's search, and it is wrong by two orders of magnitude.** A reader who
searches for a subject and narrows by facet -- the single most ordinary thing anyone does
in a library catalog -- sees 0.6% of the matching works. There is no error, no warning,
no pagination hinting at more. The result count is confident and precise.

**Facets are how a queer catalog is meant to be read.** 62,602 works, a Homosaurus
subject rail, `skos:broader` roll-up (tasks/015) so a reader can start broad and narrow.
Narrowing is the product. A patron looking for lesbian poetry finds the poetry that
happens to sit in the top sixty hits for "lesbian", and concludes the collection has
almost none.

**Nothing catches it.** The failure is silent by construction: the module's contract is
*"if the reader or artifacts are unavailable, the static list stays and nothing
regresses"*, so browse degrades quietly on purpose. Every check passes -- the page is
200, the WASM boots, Range requests answer 206, no console error, and by the time anyone
looks, the rail's counts match the result set exactly, because the click rewrote them.
`probe_opac_browse.mjs` in libcat-e2e passed 6 of 7 before I compared the UI against the
reader underneath it, and its one honest check -- rail count versus delivered count --
only fired once it read the rail *before* the click rather than after.

**The fix is one argument.** `search(q, 0, LEN, 0, filters)`. The engine already does
this correctly; the module opted out of it.

## Expected

- **Pass the filters to `search`, and stop capping the base set.** `catalog.search(q, 0,
  LEN, 0, filters)` returns the true ranked, filtered ids and their `facetCounts` in one
  call -- verified: it returns 8307 for `lesbian` + `LGBTQ+ people`. Then
  `records.getMany(ids.slice(0, PAGE))` renders the first page of a correct set, which is
  what `PAGE` should have meant all along.

- **Separate the three uses of `PAGE`.** A render page size (60), a base-set cap (large,
  or none), and a display threshold are three constants. Name them.

- **Decide what happens past 60 results.** Today there is no pager in browse mode: the
  static list is replaced by exactly `PAGE` cards, and the *static* pager stays on screen
  pointing at unfiltered pages. Either drive it (`search(q, offset, PAGE, ...)` already
  supports paging -- that is what `offset` is for) or hide it while browse owns the list,
  and say plainly "showing the first 60 of 8,307". Leaving a visible control that silently
  drops the reader's query is worse than having none.

- **Fix the `+`.** Once the total is exact, `total >= PAGE` is a statement about how many
  cards were rendered, not about how many results exist. Drop it, or make the label say
  what it means.

- **Restore the rail's promise, and test it in the order that breaks it.** Once the base
  set is the full ranked match set, `base.counts` and `base.ids` describe the same works
  again and both branches agree. Two assertions have teeth, and neither is the one that
  looks obvious:

  - `|UI results| == |search(q, 0, ∞, 0, filters)|` -- the UI against the reader.
  - the facet row's count, **sampled before the click**, equals the result count after it.
    Sampled after, it is trivially true.

  `hugo/`'s existing suites (`negatives_test.cjs`, `sidebar_test.cjs`) mock the reader, so
  none of them can see either.

- **Do not let a rail count be silently overwritten by a smaller one.** Even after the
  base set is fixed, a count that changes between advertisement and delivery is the
  signature of this class of bug, and today nothing would notice. It is worth an assertion
  in its own right.

## Repro

```bash
cd ~/libcat-e2e && node harness/probe_opac_browse.mjs   # B4, B7
cd ~/libcat-e2e && node harness/retest.mjs              # check t281
```

Read-only against the published catalog on :8502. The probe never writes anywhere and
never touches :8481 or :8501.

`B7` is the check with teeth: it boots a **second, independent** `RrsCatalog` from the
same artifacts inside the page, asks it for `search(q, 0, 100000, 0, filters)`, and
compares that number with the one the UI rendered. This is ground truth from the OPAC's
own engine, so no cross-engine comparison is involved.

Its controls carry the argument. `B0` shows the page is wired for browse. `B1` shows the
reader booted over Range requests (24 responses were 206), so a small result set is not
"the reader never started". `B2` shows the cold corpus is 62,602. `B6` shows clearing
query and filters restores that number, so `B4`/`B7` measured a filtered set rather than
a catalog that had quietly emptied.

`B4` is the rail's own promise -- it reads the facet row's count **before** clicking it,
then compares with what is delivered: promised 8307, delivered 51. It **fails**, and it
fails only because it samples before the click. Sampling after the click, which is the
natural way to write it, measures the number the click just wrote and always passes. That
is the trap this bug is built out of, so `B8` and `B4` are both kept.

By hand, in the browser console on `http://localhost:8502/works/`:

```js
const m = await import("/lcat/roaringrange.js"); await m.default();
const b = "/search";
const cat = await m.RrsCatalog.openAll(b+"/browse-index.rrs", b+"/browse-facets.rrsf",
                                       b+"/browse-records.idx", b+"/browse-records.bin");
const F = [["subject", "https://homosaurus.org/v5/homoit0000915"]];

(await cat.search("lesbian", 0, 60,     0, [])).ids.length   // 60     <- the UI's base
(await cat.search("lesbian", 0, 100000, 0, [])).ids.length   // 9923   <- the query's matches
(await cat.search("lesbian", 0, 100000, 0, F )).ids.length   // 8307   <- the true answer

// facetCounts ignore `len` entirely -- both of these print 8307:
const sub = (r) => r.facetCounts.find(f => f.field === "subject")
                    .cats.find(c => c.name === F[0][1]).count;
sub(await cat.search("lesbian", 0, 60,     0, []))           // 8307   <- what the rail shows
sub(await cat.search("lesbian", 0, 100000, 0, []))           // 8307
```

Then type `lesbian` in the search box. The **LGBTQ+ people** row reads `8307`. Click it:
the page says `51 results`, and the row now reads `51`.

## Outcome

Fixed in **v0.122.0**, commit `bbcbe68`. Every bullet under **Expected** shipped.
The report's diagnosis was correct in every particular, including the mechanism,
the three jobs `PAGE` was doing, and why nothing caught it.

### Reproduced first, on a fixture

`:8502` is not this repo's to depend on, and the e2e's `fixture-catalog.json` has
three works -- with a page size of 60 it cannot tell "the base set is the match
set" from "the base set is its first page". So: a 600-work catalog where every
number is a different number (400 match the query, 300 match query+facet, 500
carry the facet). Driven through the real page, before any change:

    UI  query only:   count="60+ results"   cards=60   rail=300
    UI  query+facet:  count="60+ results"   cards=60   rail=60
    reader truth:     query 400,  query+facet 300

The rail promised 300, delivered 60, then rewrote itself to 60 -- the reported
sequence exactly, and `"60+ results"` was wrong twice over.

### The fix

The base set is the whole ranked match set: `search(q, 0, allIds.length, 0, [])`.
A query cannot match more docs than the corpus holds, so that bound is exact and
never truncates -- which is what makes the total exact.

One thing the report did not anticipate. `search()` returns record bytes for
**every id it returns**, so simply raising `len` would decode ~10k records per
keystroke to render sixty cards. The catalog is now opened *without* the record
sidecar (`open` + `openFacets`, not `openAll`); the separate `RrsRecords` handle
already fetched exactly the page being rendered, and now does so on both paths.
That also removes a duplicate record store the module was holding open.

I kept `filters` out of `search()` rather than passing them as the report
suggests. Passing them returns the filtered ids and their counts in one call, but
the drill-down recount -- each active field recounted with *its own* selections
removed -- would then need a separate `search()` per active field. With an
unfiltered base, the existing `filterIds(base.ids, ...)` machinery is already
correct and unchanged. Same result, one call, one shape.

`PAGE` keeps exactly one job: the render slice. The `+` is gone. A list that is a
page of a larger set says so.

### The secondaries

**The pager.** It pages the server-rendered, unfiltered corpus. Hidden while
browse owns the list, restored when query and facets clear. Driving it is the
better answer -- `search(q, offset, ...)` takes an offset -- but that is a feature
with its own URL/a11y surface, and a visible control that silently drops the
reader's query is worse than none in the meantime.

**Found while fixing it, unreported:** the browse labels
(`data-lcat-noresults`, `data-lcat-resultsword`) were **never emitted by any
template**. The script's English fallbacks shipped to every language, so `/es/`
read "60+ results". They are i18n keys now (`browseNoResults`,
`browseResultsWord`, `browseShowing`), verified rendering as "mostrando los
primeros {shown} de {total} {results}" on the Spanish build.

### After

    UI  query only:   count="showing the first 60 of 400 results"   cards=60   rail=300
    UI  query+facet:  count="showing the first 60 of 300 results"   cards=60   rail=300

### Coverage, and proving it can fail

`hugo/e2e/browse-scope.spec.mjs`, wired into `run.sh` as a third pass over the
600-work catalog, with `pagerSize = 2` forced so a static pager is on screen --
otherwise the "browse hides the pager" check passes because there is no pager.
(It did. The check caught its own vacuity on the first run, because I asserted the
pager is visible *cold* before asserting it is hidden.)

Ground truth comes from a second `RrsCatalog` booted in-page from the same
artifacts, so no cross-engine comparison is involved. The two assertions with
teeth are the report's: the UI's total against the reader's, and the facet row's
count **sampled before the click** against what is delivered after it.

Against the old module, **8 of the 18 checks go red**:

    FAIL - query-only total is the match set: 60+ results
    FAIL - query+facet delivers what the rail promised: 60 == 300
    FAIL - rail is not overwritten by a smaller count after the click (300 -> 60)
    FAIL - static pager is hidden while browse owns the list
    …

The two that pass in both worlds are the controls, and they are informative:
"rail promises the truth before the click (300)" passes because the *engine* was
always right, and "renders one page of cards" passes because sixty cards is
correct either way. The bug was never in the counts or the rendering. It was in
what the module thought `ids` meant.

Gates: `npm run test:js`, a11y (124 pages), link_check, the existing browse and
browse-minimal e2e passes (17 + 16), and a default-engine build that gains no
browse attributes.

### Not done

Browse still has no pagination. Past the first 60 the reader is told the total and
given no way to reach the rest. `search(q, offset, PAGE, ...)` supports it; it
wants a task of its own.

### Independent e2e verification (libcat-e2e, 2026-07-10)

Verified read-only against the published queerbooks OPAC on `:8502`, driving the real WASM
reader in a real browser. **`probe_opac_browse.mjs`: was 6/9, now 9/9.** `t281` flips
`STILL-BROKEN -> FIXED`.

```
cold corpus                                  62602 works
"lesbian"                                     9923 matches   (rail: "showing the first 60 of 9923 results")
+ facet "LGBTQ+ people"   rail promised       8307
                          UI delivered        8307
   search('lesbian', 0, 1000000, 0, [[f,c]])  8307   <- the same reader, the same artifacts
```

The rail's promise is now kept: 8307 before the click, 8307 after. `B7`'s counterfactual holds
too -- a `PAGE`-bounded base set would have offered only 60, which is the number the bug
produced. And `B8` passes: the static paginator is hidden while browse owns the list (tasks/303).

**This fix was live for a full cycle before the harness could see it, and the fault was mine.**
`t281` and the probe both read the count line with `replace(/[^\d]/g, '')`. The fix changed that
line from `"8307 results"` to Hugo's `"showing the first 60 of 8307 results"`, so stripping
non-digits concatenated the two numbers into **608307** -- and the check dutifully reported that
the UI "delivered 608307" where the reader said 8307.

The tell was sitting in the failure message the whole time: **608307 is larger than the 62602-work
corpus.** A filtered result set cannot exceed the catalog it filters. An impossible number is a
parse error, not a defect. `t281` now asserts that invariant explicitly and reports `ERROR` rather
than `STILL-BROKEN` when a count exceeds the corpus, and the parser moved into
`ui/lib.mjs:browseTotal`, which tokenizes the numbers and takes the largest (`total >= shown`
holds in any locale).

Two cycles ago I re-ran this probe after a queerbooks rebuild, saw 5/9, and wrote *"back to its
usual 5/9, so 281 is still broken."* It had been 6/9 at filing. The count had moved and the
failing set had grown, and I read a familiar-looking number as confirmation. **A score is not a
result.**
