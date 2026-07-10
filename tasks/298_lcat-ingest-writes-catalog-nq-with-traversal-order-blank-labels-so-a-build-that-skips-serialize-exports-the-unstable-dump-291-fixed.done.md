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

## Outcome

Fixed in **v0.121.2** (patch), commit `fdd26fb`. Ask 1 shipped, with a piece of
ask 3 as a backstop. The report's diagnosis was exactly right, and one thing more
was true than it said.

### Three writers, not two

`grep 'Create("catalog.nq")'` finds **three**: `SerializeGrains` (fixed by 291),
and `BuildWorks` *and* `BuildCorpus` -- the native-BIBFRAME and MARC ingest paths.
Both re-encoded through one `rdf.Encoder`. Only the first was fixed.

### Why ask 1 rather than ask 2

Ask 2 (ingest stops writing it) is the cleaner model on paper, and I nearly took
it. But `--out` is documented as producing `catalog.nq`, `lcat project --catalog`
consumes it, and removing it would break every pipeline that ingests then
projects without serializing -- a louder break than the silent one being fixed.

Ask 1 turns out to cost nothing, because **both builders already hold each grain's
canonical bytes**: they compute them, write them to disk, and were then throwing
them away to re-serialize the graphs. They now write those same bytes through a
shared `bibframe.WriteMergedGrain`, which `SerializeGrains` also uses. One merge
path, three callers, one meaning for the file.

A pleasant side effect: `BuildWorks` was keeping every Work's `*rdf.Graph` alive
until the bulk write. It now keeps the canonical bytes instead, which are smaller.

### The invariant, and it is testable

`lcat serialize` after a single-provider ingest is now a **byte-for-byte no-op**.
That is the whole bug expressed as one property, and four new tests in
`bibframe/mergedcatalog_test.go` hold it: each builder's `catalog.nq` must equal
`SerializeGrains` over the grains it just wrote, must carry no `_:b<n>` label, must
carry editorial statements through the grain (the old `BuildWorks` appended those
separately, after the re-encoded feed lines), and serialize-after-ingest must
change nothing.

Reverting the two ingest writers turns all four red, reporting the reporter's own
symptom:

    catalog.nq carries traversal-counter labels: " _:b1 <feed:overdrive> .\n_:b1 …"

### The backstop, from ask 3

`export` gzips a `catalog.nq` it did not write, and a tree left by an older `lcat`
still holds the churning dump. `copyGzip` already scans every line for the
provenance filter, so detecting a traversal label there is free. It now warns once,
and names the command that fixes it:

    export: …/catalog.nq carries traversal-order blank-node labels, so its sha256
    will move on the next rebuild even for a catalog that did not change. … 
    Regenerate it from the grains: lcat serialize --dir …

Two tests: it fires on a stale file, and it does **not** fire on what ingest writes
today -- a warning that cries wolf teaches operators to ignore it.

`lcat export --in`'s help said "ingest output root (contains catalog.nq and
data/works)", which is the sentence that set the trap. It now says "grain root
(contains data/works and the catalog.nq derived from it)".

### Verified end to end, on the CLI

    lcat ingest --provider marc …          -> catalog.nq starts _:w3nabvv5r8eqic_c14n0
    lcat serialize --dir …                 -> sha256 unchanged (2b0082f4…)
    lcat export (straight after ingest)    -> catalog.nq.gz  88885dbb…
    lcat serialize; lcat export again      -> catalog.nq.gz  88885dbb…   (stable)

Against a HEAD build of `lcat`, the same corpus gives `_:b1` and a serialize step
that rewrites the file -- the reported bug, reproduced before fixing it.

**The RDF is unchanged.** I could not compare old and new dumps directly: two
ingest runs mint different work ids, so a cross-run diff is void (the same trap
that made my first tasks/291 export test fail). Comparing each tree against its own
grains instead: both old and new `catalog.nq` carry 2,063 statements and 468
distinct blank nodes, and each is a faithful merge of the grains beside it (same
statements modulo blank naming, blanks preserved 1:1). Only the labels moved.

### On the note you corrected

You are right: `w1dh6vtir43o8i.nq` is the playground's grain, and the 3,013
grains / 1,769,694 statements figures are the playground's too. My tasks/291 note
said "your corpus". The cross-graph blank-node split it describes is real, but it
does not occur in yours -- your 0 such grains and unchanged 1,093,632 distinct
blanks are consistent with that, and so is this fixture, whose blank nodes are
preserved 1:1 through the merge.
