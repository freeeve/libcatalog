# 108 -- export: full-corpus exports materialize the whole output (CSV: three copies) in RAM

> Filed from the 2026-07-05 full-code review. Memory is the known system wall
> (~57KB/work RSS, tasks/085).

## Symptom

A `Selection.All` export job holds the entire serialized catalog in memory before
writing -- many GB at the stated millions-of-works scale. CSV is worst: the
merged corpus N-Quads, the fully projected `Catalog`, and the CSV buffer coexist.

## Cause

- `emitNQuads`/`emitMARC`/`emitJSONLD`/`emitCSV` (backend/export/run.go:173-321)
  each return one complete `[]byte`/`bytes.Buffer`, and `Run` (run.go:44-49)
  passes it whole to `s.blob.Put`.
- `emitCSV` (run.go:245-266) first concatenates every grain's quads into one
  `merged bytes.Buffer`, then `project.Project(merged.Bytes(), ...)`
  (project/project.go:382) builds a `*Catalog` holding every Work, then renders
  the CSV -- three full-corpus copies at peak.

## Fix sketch

Give `blob.Store` (or the export path specifically) a streaming write --
temp-file spool or a `Put(io.Reader)` variant (S3 supports multipart) -- and
emit per-grain incrementally. For CSV, project incrementally per grain instead
of via one whole-corpus `Project` call.

## Acceptance

- A full-corpus export's peak RSS is bounded (per-grain working set, not
  output-sized), demonstrated against a large seeded store.
- CSV export no longer builds the merged corpus buffer and the full Catalog
  simultaneously.

## Status (2026-07-05 session)

Done, on top of the `blob.StreamPutter`/`blob.PutStream` capability that
landed with tasks/110 (DirStore streams into its rename temp file; blobs3
spools to a local temp file for a seekable upload body).

- `Run` pipes the emitters straight into PutStream; an emit failure aborts
  the pipe before the store commits anything, and `OutputPath` is only set
  on success. Every emitter became a writer-streaming `emitTo` dispatch:
  N-Quads and MARC write per grain/record (N-Quads keeps the shared encoder,
  so the full-selection export stays byte-identical to SerializeGrains --
  the existing test still pins that); JSON-LD emits per record in the
  MarshalIndent shape instead of accumulating every doc.
- CSV projects **per grain** instead of merging the whole selection and
  projecting once -- the merged buffer, the full `Catalog`, and the CSV
  buffer no longer coexist (nothing output-sized accumulates at all).
  Behavior note: a subject label asserted only in a *different* work's grain
  no longer applies to this work's row; grains carry their own works'
  statements, so real corpora are unaffected.
- Bounded memory pinned by `TestEmitCSVStreamsWithBoundedMemory` (20k-work
  seeded store; heap growth after the export must stay under 4MB).

Not done here: `Open`/the download handler still buffers the stored output
when serving without a Signer (S3 deployments presign; dir deployments read
the blob whole) -- a streaming Get is a separate, smaller seam if it ever
matters. `emitAuthorities` stays buffered (bounded by the loaded vocabulary,
not the corpus).
