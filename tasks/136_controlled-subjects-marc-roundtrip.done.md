# 136 -- controlled subjects vanish from MARC export (no 650/$0 round-trip)

Filed from queerbooks-demo (2026-07-06). Do not let a queerbooks session edit
this repo -- implement here.

## What happens

The ingest emission writes a controlled subject as `<work> bf:subject
<authority-iri>` plus `<iri> skos:prefLabel "..."@lang` (+ skos:broader) in
the feed graph (bibframe/graphcorpus.go). libcodex's MARC crosswalk
(subjectFields in reader_crosswalk.go) reads `rdf:type Topic/Place/...` +
`rdfs:label` + `bf:source` -- none of which the emission writes -- so every
controlled subject silently disappears from DecodeGrainMARC output: the
backend export's .mrc/.xml and any consumer's MARC derivation ship without
650s. queerbooks-demo measured it: 0 x 650 across 54,763 records, 268,226
after a workaround.

## Workaround in queerbooks (reference)

qbd export pre-processes each grain (cmd/qbd/subjects.go there): for every
bf:subject object carrying skos:prefLabel but no rdfs:label, add `rdf:type
bf:Topic`, `rdfs:label <prefLabel en>`, and `bf:source <scheme-iri>` (with an
rdfs:label naming the scheme) before DecodeGrainMARC. Yields
`650 _7 $a Label $2 homosaurus|fast`.

## First-class shape (decide here)

- Either the emission also writes the crosswalk-readable triples, or (nicer)
  DecodeGrainMARC/libcodex learns the SKOS shape natively.
- MARC-correct output should also carry `$0 <authority-iri>` (subjectFields
  currently never emits $0) -- that is the actually-valuable part for ILS
  consumers, and it also unlocks re-INGESTING such MARC without losing the
  authority link.
- skos:broader could map to nothing for now (subdivisions are a different
  axis); document the choice in docs/marc-fidelity.md either way.
