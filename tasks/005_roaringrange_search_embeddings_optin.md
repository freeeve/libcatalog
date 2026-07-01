# 005 — roaringrange search (embeddings off by default)

Framework core. `docs/ARCHITECTURE.md` §8; `docs/ROADMAP.md` Phase 2.

## Goal

Default search is roaringrange **lexical/BM25** — no paid AI, no Bedrock. The
vector/embedding arm is a build flag, off by default.

## Approach

- `search/` wraps roaringrange:
  - **Default (lexical):** the Rust crate's `terms` feature (BM25) + the WASM
    reader in the browser; multilingual-aware. Built from the projector's search
    index output. This is the zero-config path an adopter gets.
  - **Opt-in (semantic):** the vector arm (model2vec / provider embeddings, knn,
    similarity graph) behind a flag; enabling it requires the deployment to
    configure an embedding provider and accept the cost. Never implicit.

## Acceptance

- A default-config build produces a working lexical catalog with **no** calls to
  any embedding/AI provider.
- Enabling the flag reproduces a semantic-ranked build.
- Both paths share one query entry point; lexical-only degrades gracefully (no
  dangling vector references when off).

## Refs

- roaringrange `../roaringrange/{rust,go,python}` — `terms` (BM25), WASM reader;
  `python/scripts/model2vec_export.py` (embeddings).
- Extraction reference: qllpoc `cmd/roarindex`, `tools/semantic`.
