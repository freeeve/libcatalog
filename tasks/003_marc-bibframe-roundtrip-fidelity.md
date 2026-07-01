# 003 -- MARC <-> BIBFRAME round-trip fidelity + known-loss list

## Problem
ROADMAP Phase 0 promises MARC/MODS/schema.org export. But MARC <-> BIBFRAME
conversion is lossy in **both** directions (LC's own converters drop data). "No
lossy intermediary" (ARCHITECTURE.md §2) is true of the markdown-vs-graph point
and must not be read as round-trip fidelity. Adopters bringing their ILS's MARC
will judge the framework on exactly this, so the loss must be measured and
documented, not assumed away.

## Scope
1. **Round-trip harness.** MARC -> `codex.Record` -> BIBFRAME -> MARC over the
   ~6,266 qllpoc records; diff input vs output at the field/subfield level.
2. **Known-loss list.** Enumerate fields that do not survive round-trip (and the
   reverse: BIBFRAME constructs with no MARC home). Publish it in docs so it is a
   contract, not a surprise.
3. **Golden files.** Freeze a representative sample as golden round-trip
   fixtures; CI fails on unexplained fidelity regressions.
4. **Direction coverage.** Same for MODS and schema.org exports (at least a
   smoke-level fidelity statement).

## Acceptance
- A published known-loss table backed by the harness output.
- Golden round-trip tests in CI; a fidelity regression breaks the build.
- Phase 0 sign-off cites measured fidelity numbers, not "immediately."
