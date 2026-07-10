# 302 -- more-like-this rail dedupes by resolved label, so cross-scheme synonyms collapse in English but not in other languages

Filed from queerbooks-demo on 2026-07-10 (cross-repo ask).

Adopting v0.121.2. **296 is fixed and the fix is excellent** -- resolving `shared`
against the `terms` sideband labels all 698,114 of our shared IRIs, and our render
now shows **0 raw IRIs across all 113,988 rail pages**, down from 17,494. The
terms-sideband route was better than any of the three we proposed.

Two things survive, both in the same line.

## 1. The synonym collapse is English-only

296's second fix dedupes "by resolved label". That is language-dependent, and the
two schemes disagree about which languages they have.

On the same work, same neighbour, v0.121.2:

    en:  Shares: Elledge, Jim, Gay men, Lesbians
    es:  Comparte: Elledge, Jim, Gay men, Lesbians, Hombres gay, Lesbianas

`fast/939117` has an `en` label ("Gay men") and no `es`, so it falls back to
English. `homoit0000506` has an `es` label ("Hombres gay"). Two IRIs for one
concept resolve to two different strings, so nothing collapses -- and the Spanish
reader sees each concept twice, once in a language they did not ask for.

Replicating the template's resolution over our sidecar (422,355 spans):

    spans where the es rail lists MORE terms than the en rail:  86,593  (20.5%)

The English rail is clean because both schemes happen to agree there. Any locale
whose coverage is partial gets the stutter back, which is most of them.

Dedupe on the **concept** rather than the string it happens to render as. The
sidecar already ships IRIs, and the `terms` sideband already knows the tree, so
the information is present: collapse IRIs that share a `skos:exactMatch` /
`skos:closeMatch`, or -- cheaper and probably enough -- collapse by the `en`
label and *then* render the chosen survivor in the page's language.

## 2. Labels contain commas, and the line is comma-joined

    Shares: Canadian literature, Lesbians' writings, Canadian, Anthologies,
            Lesbians' writings, Literary collections

Five distinct labels, correctly deduped. But `fast/996602` **is** the string
`Lesbians' writings, Canadian`, so the joined line cannot be parsed by a reader:
it reads as six items, two of which look identical. Contributor names have the
same shape -- `Elledge, Jim` is one term that reads as two.

    spans containing at least one label with a comma in it:  185,153  (43.8%)

Our first pass at auditing this actually mis-flagged these as duplicate-dedupe
failures; they are not. But if a comma-split confuses a script, it confuses a
person.

Suggest emitting one element per term (`<li>`, or `<span class="lcat-shared-term">`)
and letting CSS supply the separator, so a label that contains a comma is still
one visible unit. A `·` or a middot join would also do.

## For the record

- 0 raw IRIs, both locales, all 113,988 rail pages (was 17,494 pages / 59,076 spans)
- 0 shared values our catalog cannot label -- the terms sideband covered every one
- `similar.json` unchanged, as your note promised (`e7ff6abe…` before and after)
- `catalog.nq.gz` **byte-identical across v0.121.0 -> v0.121.2** (sha256
  `4bebf7f9…`), the first release in this session that left the dump alone. 291
  delivers exactly what it promised.
- 298: ingest's `catalog.nq` now matches serialize's byte-for-byte, and both match
  what v0.121.0's serialize produced. Three writers, one answer.
