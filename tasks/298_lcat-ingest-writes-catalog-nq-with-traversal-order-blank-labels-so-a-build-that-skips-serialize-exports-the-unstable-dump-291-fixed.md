# 298 -- lcat ingest writes catalog.nq with traversal-order blank labels, so a build that skips serialize exports the unstable dump 291 fixed

Filed from queerbooks-demo on 2026-07-10 (cross-repo ask).

Found while verifying 291 on v0.121.0. **291 itself is correct** -- this is about a
second writer of the same file that the fix did not reach.

## What happened

Adopting v0.121.0, we re-ran our usual pipeline:

    lcat build --only ingest
    lcat build --only project,export,index

and the dump was *still* `_:b1, _:b2, …`, and its sha256 *still* moved. It looked
like 291 had not landed. It had; we were exporting a file 291 never touches.

`bibframe.SerializeGrains` -- the fixed writer -- runs in the **serialize** step.
But **ingest also writes `data/out/catalog.nq`**, through the old `rdf.Encoder`
path, and `export.Run` does `copyGzip(filepath.Join(opts.In, "catalog.nq"), …)`.
So the file `export` gzips is whichever writer wrote it last.

In a full `lcat build`, serialize runs right after ingest and overwrites it, so
the shipped pipeline is fine. Any build that runs ingest without serialize --
`--only ingest`, then `--only project,export,index` later -- exports ingest's
copy, with traversal-order labels and the churn 291 fixed.

Confirmed on our corpus (62,602 grains):

    after --only ingest        head data/out/catalog.nq  ->  _:b1 …
    after --only serialize     head data/out/catalog.nq  ->  _:w00071si1a8tiq_c14n4 …

Both files hold the same RDF: erasing labels and sorting gives the same hash
(`cef11daf…`), 5,458,350 statements, 1,093,632 blank nodes either way.

## Why it is worth closing

`lcat export --in` documents its argument as "ingest output root (contains
catalog.nq and data/works)", which reads as *ingest produces the catalog.nq that
export consumes*. That is exactly true, and exactly the trap: the file is real,
current, and wrong for this purpose. Nothing warns.

The failure is silent and it is the one 291 was filed about -- a published 60MB
download whose sha256 moves for a corpus that did not.

## Ask

Any of:

1. **Have ingest write the grain-derived labels too**, via `RelabelGrainBlanks` /
   `GrainBlankPrefix`. Then `catalog.nq` means one thing whoever wrote it.
2. **Have ingest not write `catalog.nq` at all** and let serialize own it. The
   build-step comment already says serialize is what "restores the full corpus"
   after multi-source ingests, so ingest's copy is only ever an intermediate.
3. **Have `export.Run` serialize from `data/works` itself** rather than trusting
   a file it did not write, or at least detect `_:b`-style labels and say so.

Option 2 seems closest to the model you already describe: the grains are the
store, catalog.nq is derived, and only one thing should derive it.

## For our part

We fixed our own loop -- it now runs `ingest,serialize,project,export,index`. The
skip was ours. But it took reading `export/export.go` to find out that the file we
were exporting was not the file 291 fixed, and the next person will lose the same
hour.

### Verified after adding serialize

- serialize is byte-stable across runs; two exports of one store are byte-equal
- a sampled grain's 106 lines appear verbatim in the 5,458,350-line merged dump
- our corpus has **0** grains stating a blank node in two graphs, so the
  cross-graph duplicate bug your note describes does not apply here and the
  distinct-blank count is unchanged at 1,093,632 (your note attributes
  `w1dh6vtir43o8i.nq` to "your corpus" -- that grain is not in ours; the 3,013
  grains / 1,769,694 statements figures look like the playground's)
