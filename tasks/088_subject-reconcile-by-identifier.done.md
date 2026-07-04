# Subject lookup: reconcile by $0 identifier, not just label

External 6XX headings from German-language targets (K10plus, DNB) never
whole-heading-match the English-labeled loaded vocabularies, so every GND
heading fell back to "adds as tag" even when a loaded term already meant
exactly that concept.

The lookup now reconciles by identifier first:

- `headingOf` collects 6XX `$0` values, folding the `(DE-588)X`
  parenthetical form to its `https://d-nb.info/gnd/X` URI; full http(s)
  URIs pass through; other control numbers (local PPNs) are dropped.
- `vocab.Index` gains `MatchIdentifier`: a snapshot-time reverse map from
  canonicalized identifier URIs (each live term's own URI, then its
  skos:exactMatch, then closeMatch siblings -- strongest tier wins, merged
  terms excluded, http/https and trailing slashes folded) to the term.
- Candidates carry their `ids` in the response; identifier matches outrank
  label matches, so a German GND heading lands on the English-labeled
  wikidata/homosaurus term when a mapping exists.

Verified end-to-end against live K10plus: imported Krieg und Frieden
(ISBN 9783446235755) via copycat, seeded a wikidata term with
`skos:exactMatch <https://d-nb.info/gnd/4041216-7>`, and the lookup
returned `650 "Napoleonische Kriege" -> wikidata:Napoleonic Wars` while
unmapped GND headings still fall back to tags (with their ids exposed).
