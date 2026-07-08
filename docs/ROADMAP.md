# libcat roadmap

Sequencing rule: **prove the model, keep qllpoc shipping, swap components last.**
Never change the source of truth, the renderer, and the backend at the same
time. Phases 0--4 are tracked as `tasks/038`--`044` in the qllpoc repo (the
reference implementation and proving ground).

## Phase 0 -- Keystone crosswalk

Record -> `codex.Record` -> `bf:Work`/`bf:Instance`, validated on all ~6,266
qllpoc records; emit canonical per-Work N-Quads grains + a bulk N-Quads dump (the
bulk `catalog.nq` is re-serialized from the grains with corpus-wide unique blank
scope). Proves
BIBFRAME can represent the real corpus and yields MARC/MODS/schema.org export --
with a documented known-loss list, since MARC<->BIBFRAME round-trips are lossy in
both directions (see `tasks/003`).

Dependency status: **libcodex 0.4.0** supplies the N-Quads reader/writer the
grains and bulk dump need (`rdf.Encoder` / `Dataset.NQuads` / `ParseNQuads` /
streaming `DecodeQuad`). **Dataset canonicalization (RDFC-1.0) has now landed**
too (libcodex `tasks/036`, shipping in **v0.5.0**): `rdf/canon.go` gives
`Dataset.Canonical()` with canonical blank-node labeling + statement sort, so an
unchanged grain re-serializes byte-for-byte (validated against the 65 W3C
rdf-canon vectors + isomorphism/idempotence/fuzz). Both halves Phase 0 needs --
N-Quads I/O and canonical output -- are in place once libcat requires libcodex
v0.5.0; grains are written through `Canonical()`, while the raw `NQuads()` stays
insertion-order for the streaming/bulk fast path.

Acceptance gates: RDFC-1.0 canonicalization is stable (re-serialize == no-op
diff) and round-trip fidelity is measured, not assumed.
-> qllpoc `tasks/038`.

## Phase 1 -- Identity + graph-as-truth

Two-tier Work/Instance ids; cluster instances into works (OpenLibrary +
computed key). Establish the git graph with named-graph provenance; migrate
qllpoc's curated overlays (`homosaurus_subjects`, `curator_*`) into `editorial:`
triples. Availability excluded.
-> `tasks/039`, `tasks/040`.

## Phase 2 -- Tier 1 static framework

Bootstrap libcat: projector CLI + Hugo module (content adapter, ported
templates/assets). qllpoc renders from the graph via the module -- first as a
transitional bridge (graph -> projected data -> Hugo module), retiring the
frontmatter/markdown pipeline. roaringrange lexical search wired in, embeddings
off by default.
-> `tasks/041`, `tasks/042`.

## Phase 3 -- Providers + MARC onboarding

Provider interface; OverDrive as the reference provider; MARC import via
libcodex as the "bring your ILS" Tier-1 ramp. qllpoc's OverDrive ingest moves
behind the interface.
-> `tasks/043`.

## Phase 4 -- qllpoc as an implementation

qllpoc depends on libcat; QLL-specifics (Homosaurus config, OverDrive
config, branding/theme) become a thin implementation layer. The
framework/implementation split becomes real.
-> `tasks/044`.

## Phase 5 -- Tier 2 generalization (later)

Parameterize the cataloging backend (review app / API / committer / auth) for
multi-tenant or self-host. Decide self-hosted vs SaaS distribution.

## Phase 6 -- Second adopter

Onboard a non-QLL library (MARC import, own vocabulary/theme). The real test of
"generic," and the bus-factor / community payoff.
