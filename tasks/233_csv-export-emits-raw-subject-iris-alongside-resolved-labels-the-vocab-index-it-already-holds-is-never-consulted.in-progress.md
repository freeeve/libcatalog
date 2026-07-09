# 233 -- CSV export emits raw subject IRIs alongside resolved labels; the vocab index it already holds is never consulted

Filed from libcat on 2026-07-09 (cross-repo ask). Confirmed as a bug by Eve:
controlled terms should resolve to labels.

## Symptom

The `subjects` column of a CSV export mixes human labels with raw IRIs, in the
same cell. A CSV export of the whole 8481 demo catalog (31 records):

```
subject values that are raw IRIs: 15    resolved labels: 2
rows mixing both: 1
```

The mixed row, verbatim:

```
w1dh6vtir43o8i,"River of teeth",…,"http://id.loc.gov/authorities/subjects/sh2007003716;
 http://id.loc.gov/authorities/subjects/sh93003390;
 http://www.wikidata.org/entity/Q122378215;
 https://homosaurus.org/v4/homoit0000170;
 LGBTQ+ science fiction;
 https://homosaurus.org/v4/homoit0001048"
```

Five IRIs and one label, semicolon-joined, in a column a cataloger opens in a
spreadsheet.

Every one of those IRIs resolves to a label through the *same running instance*:

```
GET /v1/terms/resolve?id=http://id.loc.gov/authorities/subjects/sh2007003716
  -> {"en":"Gender nonconformity"}                              scheme: lcsh
GET /v1/terms/resolve?id=https://homosaurus.org/v4/homoit0000170
  -> {"en":"Bisexual people", "de":"Bisexuelle Personen", …}    scheme: homosaurus
```

## Root cause

`backend/export/run.go:297-303` takes the label only from what the *grain*
happens to carry, and falls back to the IRI:

```go
subjects := make([]string, 0, len(work.Subjects))
for _, subj := range work.Subjects {
    label := subj.ID                              // <- the IRI
    if l := vocab.PickLabel(subj.Labels); l != "" {
        label = l
    }
    subjects = append(subjects, label)
}
```

`work.Subjects[].Labels` is populated by the projection from `skos:prefLabel`
statements present in the grain (`project/merge.go:41-49`), which is why the two
subjects whose authority terms were appended to the grain resolve and the other
fifteen do not. Nothing here consults the term index.

The index is already on the service. `backend/export/export.go:99-101`:

```go
// Vocab, when set, enables authority exports over the loaded term index
// (tasks/069).
Vocab *vocab.Index
```

and `backend/vocab/vocab.go:583` `func (ix *Index) Resolve(id string) (*Term, bool)`
is exactly the lookup needed -- it is what `GET /v1/terms/resolve`
(`httpapi/terms_handler.go:28`) calls, and it already handles the homosaurus IRI
variants. `emitCSV` simply never asks.

## Why it matters

CSV is the format a person opens, not a machine: it exists so a cataloger can
sort, pivot, and hand a subject list to someone who does not read RDF. A column
that is 88% opaque IRIs does not do that job, and the inconsistency is worse than
uniform IRIs would be -- sorting the column interleaves `LGBTQ+ science fiction`
with `http://id.loc.gov/…`, and any downstream `GROUP BY subject` counts the same
concept twice under two spellings.

It also silently depends on ingest history: whether a given subject appears as a
word or a URL comes down to whether its authority terms were appended to that
particular grain, which is invisible from the export.

The machine-readable formats (N-Quads, JSON-LD) carry IRIs, which is correct and
should not change.

## Expected

- `emitCSV` resolves each subject through `s.Vocab.Resolve(subj.ID)` when the
  grain carries no label, and falls back to the IRI only when the index has no
  entry either -- so a term that cannot be resolved stays visible rather than
  being dropped.
- `s.Vocab` is optional (nil today in some wirings); the fallback path must keep
  working, and CSV export must not start failing when no index is loaded.
- Worth deciding: an unresolvable IRI could be emitted as-is (today's fallback)
  or flagged. I would keep it as-is -- silently dropping a subject is worse than
  showing a URL.
- The same question applies to `contributors` (agent IRIs) if they can arrive
  unlabeled; I did not check that path.
- A test over a grain holding one labeled and one unlabeled controlled subject,
  asserting both come out as labels when the index knows them.

## Repro

```
cd ~/libcat-e2e && node harness/probe_export_csv.mjs
```

Expect `X3` (no raw IRIs in the subjects column) to flip to PASS, with `X4`
(unresolvable IRIs still emitted, not dropped) staying green. The probe only
issues an export and reads it back; it writes nothing to the catalog.
`harness/retest.mjs` carries the same check as `t233`.
