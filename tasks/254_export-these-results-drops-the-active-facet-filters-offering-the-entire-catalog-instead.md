# 254 -- Export these results drops the active facet filters, offering the entire catalog instead

Filed from libcat-e2e on 2026-07-09 (cross-repo ask).

Found on **8501** (queerbooks, 62,602 works), read-only. Nothing was mutated; no
export job was created.

## Symptom

The works screen's status line offers **"Export these results…"** next to the match
count. With facets applied, the link does not carry them.

Facets only -- the screen says 465, the link says the whole catalog:

```
url                : #/works?holdings=none&tag=poetry
works screen status: "50 of 465 matched · 62602 in catalog · Export these results… · Clear filters"
export link        : #/exports?kind=all

after following it : #/exports?kind=all
selection control  : {"value":"all","text":"Entire catalog"}
exports screen     : "Preview 62602 works"
```

465 results, one click, and the export form is preloaded with 62,602.

Query plus facet -- the facet is dropped and the query survives, so the number is
wrong by a smaller amount and is therefore easier to miss:

```
url                : #/works?q=lesbian&holdings=none
works screen status: "50 of 1526 matched · … · Export these results…"
export link        : #/exports?kind=search&q=lesbian     -> 1547 works, not 1526
```

The exports screen itself is honest: it names the selection ("Entire catalog") and
previews the count (62,602). The **link** is what lies -- it is labelled "these
results" and points at a different set. Nothing on the exports screen mentions that
`holdings=none` and `tag=poetry` were discarded on the way in.

## Root cause

`backend/ui/src/screens/WorkSearch.svelte:269`:

```svelte
<a href={st.q.trim() ? "#/exports?kind=search&q=" + encodeURIComponent(st.q.trim()) : "#/exports?kind=all"}>
  Export these results…
</a>
```

Only `st.q` is forwarded. `st.filters` -- the very thing the rail on the left is
editing -- is never consulted. When `q` is empty the link degrades to `kind=all`,
which `Exports.svelte:40-41` faithfully honours:

```ts
let kind = $state<Selection["kind"]>(
  initialKind === "ids" || initialKind === "search" || initialKind === "savedQuery" ? initialKind : "all",
);
```

`Exports.svelte:38-39` documents the `kind=all` fallback as deliberate --
*"'Export these results…' arrives with no query (tasks/197)"* -- which was right when
"no query" meant "no filter of any kind". With facets it no longer does.

The deeper reason it cannot simply be fixed in the href: **no export selection can
express a facet.** `backend/export/export.go:44-47`:

```go
type Selection struct {
	All     bool     `json:"all,omitempty"`
	WorkIDs []string `json:"workIds,omitempty"`
}
```

and `backend/batch/batch.go:56-61`:

```go
type Selection struct {
	Kind         string   `json:"kind"`
	IDs          []string `json:"ids,omitempty"`          // kind=ids
	Query        string   `json:"query,omitempty"`        // kind=search
	SavedQueryID string   `json:"savedQueryId,omitempty"` // kind=savedQuery
}
```

`kind=search` carries a free-text `Query` and nothing else. There is no
`holdings`/`tag`/`subject`/`sources`/`needs` anywhere in the export path, so today the
link has no honest target to point at when a facet is active.

## Why it matters

A cataloger narrows to a slice they care about -- a tag, a source, works needing
subjects -- and clicks the button the UI puts directly beside the count of that slice.
Depending on whether they also typed a search, they get the entire 62,602-work catalog
or a superset of what they asked for. A MARC or BIBFRAME export of 62,602 records is
not a small mistake to notice late, and "Export these results…" is exactly the phrasing
that earns the trust that stops someone from checking.

The exports screen's preview is the only thing standing between the user and the wrong
file, and it reads `Preview 62602 works` in a form the user believes they arrived at
with a 465-work selection.

This compounds **253**, filed alongside: there, a facet can be *applied but invisible*
in the rail. A user with an invisible `tag` filter has no way to know the export they
are about to run silently differs from what the screen shows -- the two bugs share the
same root assumption, that `st.filters` is not part of "the query".

## Expected

Pick one, in order of preference.

1. **Teach the export path about facets.** Add the facet parameters to
   `batch.Selection` (e.g. `Facets map[string][]string` for `kind=search`, which
   `compileBatchSelection` already resolves against the same work index the listing
   uses), forward them from `WorkSearch.svelte:269`, and let `kind=search` with an
   empty `Query` mean "the whole catalog, filtered". Then "Export these results…"
   means what it says.

2. **Fail loudly rather than quietly.** If facets are active and cannot be expressed,
   the exports screen should say so -- carry the facets in the hash purely to display
   *"the filters holdings=none, tag=poetry were not applied to this export"* -- so the
   count mismatch is explained rather than discovered.

3. **At minimum, stop mislabelling.** When `filtersActive` is true, either hide the
   link or relabel it ("Export the whole catalog…" / "Export this search…"), so the
   text never promises a set the target cannot produce.

Whichever way, an export's preview count and the works screen's `matched` should agree,
or the difference should be stated on screen.

## Repro

```bash
cd ~/libcat-e2e && node harness/probe_opac_facets.mjs   # F7, F8
cd ~/libcat-e2e && node harness/retest.mjs              # check t254
```

By hand, read-only on 8501: open `#/works?holdings=none&tag=poetry` (reload so the deep
link mounts cold), read `50 of 465 matched`, then hover or click **Export these
results…**. The href is `#/exports?kind=all` and the form previews 62,602 works. Do not
press Export.
