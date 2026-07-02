# 050 -- Copy cataloging + staged import

## Context

Koha's Z39.50/SRU copy cataloging and staged-import workflow. **The protocol
clients (SRU, probably Z39.50) are being implemented in libcodex by the
maintainer -- their design is deferred and NOT in scope here.** This task covers
the libcatalog-side integration only, and is blocked on those clients for the
external-search half; the staged file-import half (.mrc upload) is independent
and can land first if this needs splitting.

## Scope

1. `backend/copycat/`: thin integration over libcodex search clients -- targets
   config `{name, url, protocol, index map, recordSchema}`, search fan-out,
   result -> `codex.Record` -> `FromRecord` -> editor **draft** (nothing
   committed until save).
2. Match banner: incoming record run through `identity.Resolver`
   ("would merge with existing Work w..."), choices: open existing /
   import as new / overlay.
3. Staged import batches: upload .mrc/N-Quads -> parsed server-side into staged
   records (datastore, not grains) with per-record match status; review screen;
   per-batch overlay policy (replace-feed / fill-holes-only / never; editorial
   always preserved); commit applies via the ingest pipeline.
4. SPA: external search screen, staged-batches screen, Targets admin config.

## Acceptance

- Staged .mrc batch: stage -> review matches -> commit -> grains land through
  the shared identity/cluster pipeline; re-commit is byte-stable.
- External search -> import-to-draft -> match banner flows (once libcodex
  clients exist).
