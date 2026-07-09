# 204 -- tag promotion writes its alias grain into the authority graph namespace so `aliases` becomes a bogus vocabulary scheme that shadows the promoted term

Filed from libcat-e2e on 2026-07-09 (cross-repo ask).

Found while probing vocabularies. Triggered by an ordinary, supported action:
approving a tag promotion.

## Symptom

After any `POST /v1/promotions/decide {approve:true}`, a vocabulary scheme named
`aliases` springs into existence, and the promoted term resolves to it instead of
to its real vocabulary.

On the 8481 playground, after promoting a folk tag onto the LCSH heading
`sh2007003716` ("Gender nonconformity"):

```
GET /config
  schemes: ["aliases","homosaurus","lcgft","lcnaf","lcsh","lcshac","local","wikidata","folk"]
                ^^^^^^^ new

GET /v1/terms/resolve?id=http://id.loc.gov/authorities/subjects/sh2007003716
  { "scheme": "aliases", "labels": {} }        <- was {"scheme":"lcsh","labels":{"en":"Gender nonconformity"}}

GET /v1/works?limit=1   (facets.subject)
  { "value": "http://id.loc.gov/authorities/subjects/sh2007003716",
    "count": 1, "scheme": "aliases" }          <- an LCSH heading, grouped under "aliases"
  schemes seen in subject facet: [aliases, homosaurus, lcgft, lcnaf, lcsh, wikidata]
```

The affected work (`w1dh6vtir43o8i`) is a **pre-existing catalog record**, not a
test fixture -- it already carried that subject. The direct lookup still works
(`GET /v1/term?scheme=lcsh&id=…` -> 200 with labels); only the *resolve* path and
anything keyed off it are wrong.

## Root cause

`backend/publish/promote.go:82` `recordAlias` appends the `lcat:tagAlias`
statement to the alias grain, and at `:90` names the graph:

```go
graph := bibframe.AuthorityGraph("aliases")
...
Add: []rdf.Quad{bibframe.TagAliasQuad(promo.Term.ID, promo.Tag)},
```

The vocab loader routes terms **by that graph prefix** --
`backend/vocab/vocab.go:40`:

```go
authorityGraphPrefix = "authority:"
```

("the `authority:<vocab>` named graph (ARCHITECTURE §5), so the loader routes
terms to their scheme", `vocab/vocab.go:4`).

So `authority:aliases` is indistinguishable from `authority:lcsh` to the loader:
it registers a scheme called `aliases` whose only member is the promoted term
IRI, carrying a `tagAlias` predicate and **no `skos:prefLabel`**. On disk:

```nq
# site/data/authorities/al/aliases.nq
<http://id.loc.gov/authorities/subjects/sh2007003716> <…/ns#tagAlias> "some-tag" <authority:aliases> .
```

`ix.Resolve(id)` (`vocab/vocab.go:568`) scans schemes and returns this labelless
stub, shadowing the real LCSH entry.

## Why it matters

1. **Subject facets mis-group.** `worksList` attaches a scheme to every subject
   facet value through `schemeResolver` -> `vx.Resolve(iri)`
   (`httpapi/works_list_handler.go:18`). Every promoted term therefore renders in
   the facet rail under a bogus **Aliases** group instead of LCSH/FAST/etc --
   precisely what tasks/174 built the grouping to avoid.
2. **`/config.schemes` grows a fake vocabulary**, which the SPA renders as a real
   scheme group.
3. **`GET /v1/terms/resolve` returns a labelless term.** The picker's
   neighborhood panel resolves broader/narrower/related URIs through it
   (`terms_handler.go:36`), so a promoted term loses its label there.
4. It gets worse with use: the alias grain accumulates one statement per
   promotion, so the `aliases` scheme grows with every tag folded in.

This is the same class as the just-fixed tasks/202: **non-heading statements
sitting in an authority graph get read as headings.** 202 taught the authorities
*list* to skip labelless debris; here the debris reaches the vocab *index*.

## Expected

The alias grain is bookkeeping for the projector, not a vocabulary. It must not
register a scheme.

Options, cheapest first:

- Name the graph something outside the `authority:` namespace (e.g.
  `lcat:aliases` / `alias:tags`), so the loader never sees it. `recordAlias` and
  whatever reads the alias grain are the only call sites.
- Or, keep the graph and have the vocab loader skip `authority:aliases`
  explicitly (a denylist -- brittle).
- Or, make the loader ignore authority-graph subjects with no `skos:prefLabel`,
  which also hardens against future debris (the 202 fix, one layer down).

Guard with a test: promote a tag, then assert `ix.Schemes()` is unchanged and
`ix.Resolve(term)` still returns the term's original scheme and labels.

## Repro

```sh
# libcat-e2e
node harness/probe_vocab.mjs      # W10 shows resolve -> scheme "aliases"
curl -s localhost:8481/config | jq .schemes          # contains "aliases"
curl -s -G localhost:8481/v1/terms/resolve \
  --data-urlencode id=http://id.loc.gov/authorities/subjects/sh2007003716
```

Any approved promotion reproduces it; nothing about the sentinel tags matters.

## Cleanup owed on the playground

`site/data/authorities/al/aliases.nq` holds three `tagAlias` statements from
libcat-e2e's promotion probes (`zz-e2e-promo-*`, `zz-promo2-*`,
`zz-promofresh-*`), all pointing at `sh2007003716`. Deleting that file restores
`/config.schemes` and the subject facet. libcat-e2e is not permitted to edit the
blob store and did not.

## Outcome

Fixed in 274dd19, released v0.54.0 -- took your cheapest option AND
your hardening option together:

- recordAlias writes into a new bibframe.AliasGraph() = lcat:aliases,
  outside the authority: namespace the loader routes on. The projector
  indexes lcat:tagAlias across every graph, so alias suppression is
  untouched (verified in the projector source before renaming).
- The vocab loader prunes authority-graph subjects with no labels at
  all, and drops schemes left empty -- your option 3, the tasks/202
  principle one layer down. This also heals every existing store still
  carrying legacy authority:aliases statements with NO migration
  needed (queerbooks included).
- Your suggested guard exists at both layers:
  TestDebrisNeverMintsScheme (legacy-graph debris -> Schemes()
  unchanged, Resolve returns lcsh + labels) and the promote test now
  asserts the grain carries <lcat:aliases>.
- Playground: migrated site/data/authorities/al/aliases.nq to the new
  graph (kept your probes' suppression bookkeeping rather than
  deleting). Verified live: /config.schemes back to 8 real schemes,
  sh2007003716 resolves scheme=lcsh "Gender nonconformity".
