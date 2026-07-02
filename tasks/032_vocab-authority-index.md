# 032 -- Vocabulary authority index + folksonomy normalizer

## Context

qllpoc validated every suggested term against an embedded homosaurus-min.json.
The generalized module is vocabulary-agnostic: controlled vocabularies are SKOS
authority grains (`data/authorities/...`, ARCHITECTURE §3/§5) loaded through
`storage/blob.Store` into an in-memory term index. Folksonomy terms get a
normalizer so free text never reaches the graph unreviewed.

## Scope

1. `backend/vocab/vocab.go`: loader (scan authority grains per configured
   vocabulary; `skos:prefLabel`/`altLabel`/`broader`/`narrower`/`related` +
   definitions), `Index` with `Lookup(scheme, id)`, `Search(scheme, q)`
   (prefLabel/altLabel prefix match, lang-aware), `Broader`.
2. `TermRef{Scheme, ID, Label}` -- scheme is a configured vocab key or `folk`.
3. `NormalizeFolk(raw)`: NFKC, lowercase, whitespace collapse, control-char
   strip, length cap, URL/deny-list rejection.
4. `GET /v1/terms?scheme=&q=`: autocomplete over controlled vocab + ACCEPTED
   folk terms (folk lifecycle itself is tasks/033).

## Acceptance

- Loads a fixture Homosaurus/LCSH-subset authority grain; search and broader
  resolve; multilingual labels honored.
- Folk normalization property tests (idempotent, rejects URLs/control chars).
- Unknown scheme/id lookups fail closed.
