# 296 -- more-like-this rail renders raw authority IRIs when a shared concept is not among the page's own subjects

Filed from queerbooks-demo on 2026-07-10 (cross-repo ask).

Found adopting v0.118.0 (tasks/284's rail) over a 62,602-work catalog.

## What a visitor sees

    Shares: Juvenile Literature, Juvenile Fiction, Fantasy, Transgender people,
            https://homosaurus.org/v5/homoit0001643

A bare authority URL, in the middle of a human-readable list, on the public page.

Across the full render, both locales:

    pages with a rail:         113,988
    pages showing a raw IRI:    17,494   (15.3%)
    "Shares" spans:            844,710
    spans showing a raw IRI:    59,076   (7.0%)

## Why

`page.html` resolves shared subject IRIs against **the page's own** `subjectList`:

```gotemplate
{{- range $.Params.subjectList -}}
  {{- $labels = merge $labels (dict .id $l) -}}
{{- end -}}
```

The comment says "This page already carries the labels for its own subjects", and
that assumption is what breaks: `SimilarNeighbor.Shared` can name a concept the
page does **not** carry. Your own struct comment already says so --

    // A neighbour reached only through the concept tree or a flat bonus can
    // legitimately share nothing verbatim.

-- but the rail prints `shared[]` regardless. Checked directly: for every raw IRI
sampled, the concept is in the *neighbour's* subject list and absent from the
page's own.

    work w0094pvukkq59m -> neighbour wu01ue6cnt7o6o
      shared: https://homosaurus.org/v5/homoit0001643
      in this work's subjects?     False
      in the neighbour's subjects? True

FAST IRIs mostly resolve because both works tend to carry them verbatim;
Homosaurus concepts, reached through the tree, mostly do not. So the bug reads as
"Homosaurus URLs leak", but the cause is the tree hop, not the scheme.

## A second, smaller flaw in the same span

One concept expressed in two schemes prints its label twice:

    Shares: Elledge, Jim, Gay men, Lesbians, Gay men, Lesbians

`shared` for that pair is `["Elledge, Jim", fast/939117, fast/996540,
homoit0000506, homoit0000556]` -- the FAST and Homosaurus IRIs for "Gay men" and
"Lesbians". Both resolve, both render. The reader sees a stutter, not two facts.

## Ask

The sidecar is language-neutral by design, and the page cannot label a concept it
does not hold -- so the page is the wrong place to resolve these. Either:

1. **Have the projector resolve `shared` to labels** as it writes similar.json --
   it holds the whole catalog and every authority label. This costs size (the
   sidecar is already 103MB at 8 neighbours × 62.6k works here) unless labels are
   interned, and it needs one entry per site language, which is what the current
   design was avoiding. Or:
2. **Emit `shared` as `{id, label}` pairs, or a parallel `sharedLabels`**, letting
   the template render the label and keep the IRI in a `title`/`data-` attribute.
   Or:
3. **Drop from `shared` any value the page cannot label** -- the cheapest fix, and
   defensible: a "shares" line exists to explain the recommendation, and a bare
   URL explains nothing. A card that shares only tree-reached concepts would then
   show no reason, which is the honest outcome.

Whichever route, please also dedupe by resolved label so cross-scheme synonyms
collapse to one term.

## Adopter mitigation meanwhile

`[project] similar = 0` removes the sidecar and the rail entirely. That is the
switch we will use if we deploy before this lands, since the rail is otherwise a
good feature and we would rather not ship URLs to readers.
