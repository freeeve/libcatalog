# 306 -- 302 verified on 62602 works: es stutter 0, comma spans 214304 not 185153 (your baseline was our comma-split audit), and 0 cross-scheme homographs in 43 colliding labels

Filed from queerbooks-demo on 2026-07-10 (cross-repo ask).

Answers the "what we would like back" in your 302 note (our tasks/067). Adopted at
v0.124.0; our `v0.28.0`.

Everything below is measured by reading `span.lcat-similar-term` **elements**, never by
splitting text. 62,602 works x 2 locales, 422,355 neighbour blocks, 1,145,189 term
elements per locale.

## Both defects are fixed

- Blocks where `es` lists more terms than `en`: **0** (was 20.5%).
- Blocks where `en` lists more terms than `es`: **0**.
- Term counts agree block-for-block on all 62,602 works; `es` spans == `en` spans ==
  1,145,189 exactly.
- Raw authority IRIs inside rail terms: **0** (296 stays fixed).
- `w00071si1a8tiq`'s first neighbour reads `Comparte: Elledge, Jim · Hombres gay ·
  Lesbianas`. `Elledge, Jim` and `Criticism, interpretation, etc.` are single elements.

## Your second number: 214,304, not 185,153 -- and no label broke

Spans whose rendered term contains a comma: **214,304 per locale** (428,608 across
both). You expected ~185,153 to hold unchanged, and set that as the tripwire for a
broken label. It moved. It should have.

The render is byte-faithful to the data. Recomputing the term list straight from
`similar.json` + the `catalog.json` `terms` sideband -- resolve each shared entry,
collapse by concept, count -- gives **1,145,189 spans, 214,304 comma spans, 0
unresolved IRIs**, an exact match to the render in both locales.

185,153 is, we are fairly sure, **our own pre-fix number** -- from precisely the
comma-splitting audit you identify in the 302 note as unable to tell the fix from the
bug. It does not reconstruct from the data under any per-term definition we tried:

| definition                                     | count   |
|---|---|
| dedupe by label, per block (what renders)      | 214,304 |
| no dedupe, per block                           | 214,304 |
| dedupe per work, across blocks                 | 55,599  |
| IRI-derived terms only (contributor names cut) | 14,930  |

The 29,151 gap is not multi-comma labels either -- only 4,245 spans carry two or more
commas. So the tripwire fired on a baseline that was never well-defined (pre-fix there
was one span per block), not on a broken label. If 185,153 came from somewhere other
than our old audit, say so and we will chase it.

**Suggestion:** in the 302 note, retire 185,153 as the expected value and cite 214,304
as the element-counted baseline. Anyone else adopting will otherwise trip the same
wire.

## Homograph audit: zero in this corpus

Of 10,050 terms, 55 English labels are carried by more than one IRI; 43 of those are
cross-scheme, and every single one is a FAST <-> Homosaurus pair.

To distinguish "two names for one concept" from "two concepts sharing a name", we
measured co-assignment -- how often one work carries both IRIs of a pair:

- pairs where at least one work carries both IRIs: **26/43**
- pairs with **zero** overlap (the homograph signature): **0**
- remaining 17: no Homosaurus *subject* usage at all (they reach works via tags), so
  the subject-level test is silent on them, not negative

The collapse fires on 87,439 of 422,355 blocks (20.7%), dominated by `Gay men`
(27,942), `Lesbians` (27,373), `Gender identity` (18,514). All correct.

### The near miss worth naming

**`Sapphics`** -- FAST `1105395` + `homoit0002277`, 1,063 blocks. "Sapphics" is also the
classical verse form, so this has exactly the shape you warned about. It is not the
substance: in our corpus both IRIs sit on queer women's fiction, each is the other's
top co-subject, and FAST's usage co-occurs with `Lesbians`, `Young adult fiction`,
`Fantasy`. Both mean the people, not the metre.

So `skos:exactMatch` is **not** justified by our data today. But note this is a
property of our corpus, not of the design. Merge in a poetry or classics collection and
`Sapphics` becomes a real homograph that the English-label collapse gets wrong,
silently and with no way for an adopter to notice. If you ever do the `exactMatch`
work, that is the test case.

## Adoption notes (no action needed)

- Engine tree hashes (`cmd`, `ingest`, `project`, `bibframe`, `internal`) identical to
  v0.122.0; `catalog.nq.gz` byte-identical for the fourth consecutive release (291
  holds). We reprojected anyway and confirmed `similar.json` unchanged, as you said.
- Render churn is the `lcat.css` fingerprint plus rail markup on the 113,988 work pages
  that carry a rail. Zero unexplained bytes. Reversing the `integrity` digest to
  reconstruct old bytes needs the base64 `+` written as `&#43;` -- Hugo escapes it in
  the attribute.
- `similar_seam_test.cjs` and `publishable_names_test.cjs` pass against our data.
- 300's promotion handlers: unauthenticated `GET /v1/promotions` -> 401; authenticated
  -> 200 `{"promotions":[]}`.
