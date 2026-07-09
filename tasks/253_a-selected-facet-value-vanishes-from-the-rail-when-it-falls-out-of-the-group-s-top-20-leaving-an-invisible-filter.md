# 253 -- a selected facet value vanishes from the rail when it falls out of the group's top-20, leaving an invisible filter

Filed from libcat-e2e on 2026-07-09 (cross-repo ask).

Found on **8501** (queerbooks, 62,602 works), read-only, GET requests and browsing
only. Nothing was mutated.

## Symptom

Two clicks, both on checkboxes the rail is showing you:

```
1. click tag "ebook in qll"   -> url #/works?tag=ebook+in+qll
                                 status "50 of 4617 matched"
                                 checked boxes: ["ebook in qll 4617"]
                                 tag present in rail: true

2. click holdings "no holdings" -> url #/works?holdings=none&tag=ebook+in+qll
                                 status "50 of 149 matched"
                                 checked boxes: ["no holdings 149"]
                                 tag present in rail: FALSE
```

The tag filter is still applied -- `holdings=none` alone matches 57,112, and the page
shows 149 -- but the `ebook in qll` checkbox is gone from the rail. Searching the whole
rendered page for the string finds nothing:

```
does the page mention the hidden tag anywhere? false
```

So the rail says one filter is active; the query has two. The user cannot uncheck the
one they can't see.

It is reachable by deep link too, which is worse because the tracker records that
search URLs are shareable (OPAC_FEATURES U6). Any tag outside the global top-20 is
invisible from the moment the link is opened:

```
tag=poetry   -> matched=797   present in facets.tag: false  (facets.tag has 20 values)
tag=politics -> matched=334   present in facets.tag: false
tag=mystery  -> matched=1193  present in facets.tag: false
```

## Root cause

Two halves, and the client half is the easier fix.

**Server.** `backend/httpapi/works_facets.go:203` sets `facetTopN = 20`, and
`result()` (`:209-234`) truncates the open-ended groups -- `subject`, `tag`, and the
deployment's extras such as `sources` -- to the top 20 by count:

```go
if group.capped {
    if group.schemeOf != nil {
        list = capPerScheme(list, facetTopN)
    } else if len(list) > facetTopN {
        list = list[:facetTopN]
    }
}
```

Nothing exempts a *selected* value from the cut. Worse, a selected value whose count
is zero under the other groups' filters never enters `c.counts[g]` at all
(`:192-196` only increments for values a matching work actually carries), so it is
absent rather than merely truncated.

The reason a selection can push its own value out of its own group is the
self-excluding counter (`add`, `:180-198`): a group's counts ignore that group's own
filter but honour every other group's. So adding a `holdings` filter recomputes the
`tag` counts over a smaller population, and a tag that ranked inside the top 20
globally can fall outside it -- while remaining selected.

**Client.** `backend/ui/src/screens/WorkSearch.svelte:78-99` builds the rail purely
from the server's response:

```ts
const counts = st.facets[g.key] ?? [];
if (counts.length === 0) continue;
```

and `:286` renders a checkbox only for values in that list:

```svelte
<input type="checkbox" checked={filterActive(group.filterKey, fc.value)} … />
```

The intent to keep selected values pinned already exists one function up.
`visibleCounts` (`:101-105`) deliberately keeps a selected value visible when the
rail's own type-to-filter box would hide it:

```ts
return group.counts.filter((fcv) =>
  group.label(fcv.value).toLowerCase().includes(q) || filterActive(group.filterKey, fcv.value));
```

That `|| filterActive(...)` is exactly the right instinct, applied to the client-side
text filter and not to the server's truncation.

## Why it matters

The facet contract on this surface is otherwise excellent -- I checked 18
`(group, value)` pairs under a query plus a multi-select, cross-group selection, and
every advertised count equalled the `matched` you get by selecting it, exactly. Users
can and do trust these numbers. That is precisely why a filter that is applied but not
displayed is costly: the rail is the only account of what the query is, and it is
incomplete.

Concretely, a librarian narrowing 62,602 works to 149 sees a rail claiming only
"no holdings" is on. They read 149 as the count of *no-holdings* works, which is 57,112.
Any number they copy out of that screen is wrong, and the export they take from it
(see **254**, filed alongside) is wrong in a different direction.

Recovery exists but is not discoverable: unchecking `no holdings` restores the tag
checkbox (the tag re-enters its own top-20 once the population widens), or **Clear
filters** (`WorkSearch.svelte:272`) drops everything at once. Neither lets the user
remove the invisible filter alone, and neither tells them it is there.

I could not construct a state where *two* selected values are hidden simultaneously --
`subject` caps per scheme (`capPerScheme`), which is more forgiving -- so this is not
a dead end, only a lie.

## Expected

A selected facet value must always appear in its group, checked, with its true count
(including `0`).

Cleanest at the server: in `result()`, after sorting and capping, append any
`group.selected` value missing from `list`, with `c.counts[i][v]` (defaulting to 0).
That fixes every client at once and keeps the rail's counts honest.

Alternatively at the client: in `railGroups`, union the server's counts with
`st.filters[filterKey]`, synthesizing `{value, count: 0}` for any selected value the
response omitted. This is a smaller change and mirrors the `|| filterActive(...)` that
`visibleCounts` already does.

A per-filter "remove" affordance (chips above the results, say) would make the state
legible even when the rail is long, but the checkbox reappearing is enough.

## Repro

```bash
cd ~/libcat-e2e && node harness/probe_opac_facets.mjs   # F5, F6
cd ~/libcat-e2e && node harness/retest.mjs              # check t253
```

By hand, read-only on 8501: open `#/works`, click the tag `ebook in qll`, then click
`no holdings`. The result count drops to 149 and the tag's checkbox disappears while
`tag=ebook+in+qll` stays in the URL. Or open `#/works?tag=poetry` directly: 797
matched, and `poetry` is nowhere in the rail.

API only:

```bash
curl -s -H "Authorization: Bearer $TOK" \
  'localhost:8501/v1/works?limit=1&tag=poetry' | jq '.matched, (.facets.tag|map(.value)|index("poetry"))'
# 797
# null      <- the selected value is not in its own facet list
```
