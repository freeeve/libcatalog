# 232 -- work relations accept a contradictory pair: A can be both hasPart and partOf the same work B

Filed from libcat on 2026-07-09 (cross-repo ask).

## Symptom

Link A `hasPart` B, then link A `partOf` B. Both calls return 204, and A now
both contains B and is contained by B. The Relationships panel lists B under
"Has part" *and* under "Part of", on both works.

Measured on the 8481 playground against two copycat-minted sentinels
(`ui/probe_relations.mjs`):

```
PASS R1   a work cannot relate to itself                    -> 400 "a work cannot relate to itself"
PASS R4   add writes the forward and inverse statements     A.hasPart=[B]  B.partOf=[A]

FAIL R6   a work cannot both contain and be part of one work
          HTTP 204; A.hasPart=[B] A.partOf=[B], and symmetrically B.hasPart=[A] B.partOf=[A]
FAIL R6b  the grain holds only one direction between two works
          A's grain asserts both <#AWork> bf:hasPart <#BWork> and bf:partOf the same node
```

Because each write also lands its inverse on the target, two calls produce four
statements and leave the pair perfectly symmetric: each work claims to contain
the other and to be a part of the other.

## Root cause

`backend/httpapi/relations_handlers.go:70-108` (`mutate`) validates the kind, the
two work ids, that the target is not the work itself, and that both grains
exist. It never checks whether the opposite relation between the same pair is
already asserted:

```go
if req.Target == workID {
    writeError(w, http.StatusBadRequest, "a work cannot relate to itself")
    return
}
// ... both grains must exist ...
if _, err := mutateWorkGrain(r, bs, ix, workID, func(g []byte) ([]byte, error) {
    return bibframe.SetWorkRelation(g, workID, kind.pred, req.Target, add)
}); err != nil {
```

`bibframe.SetWorkRelation` (`bibframe/relations.go:62`) is a bare quad add/remove
over `ApplyEditorialPatch`. It guards the predicate and `grainDescribesWork`, but
has no view of the inverse predicate, so nothing anywhere rejects the pair.

The self-relation guard shows the intent -- a work must not be its own part. The
same reasoning forbids a two-work cycle; it just was not extended.

## Why it matters

`bf:hasPart` and `bf:partOf` are inverses. Asserting both between the same pair
is not a curation choice, it is a contradiction: any consumer that walks the
containment graph (a "parts of this" list, a series display, a breadcrumb) can
loop forever or render B as both parent and child of A. The statements live in
`editorial:`, so they survive re-ingest and ship with the published grain.

A cataloger reaches it in two clicks with no warning, and nothing in the panel
signals that the record is now inconsistent -- B simply appears in both lists.
There is no "repair" affordance either; they must remove the right one of two
identical-looking entries.

The blast radius is small today because `docs/marc-fidelity.md` records that 773
/ 774 are not yet shaped, so the contradiction does not reach MARC export. It
does reach the grain and the panel.

## Expected

- `mutate` refuses to add a relation when the inverse already holds between the
  same pair, with a message naming the existing link -- e.g.
  `400 "B is already a part of A; remove that link first"`.
- The check belongs next to the self-relation guard, and it must read the target
  grain (or the source grain's inverse predicate) before either write, so no
  half-link is left -- the handler already establishes that ordering discipline
  for the "both grains must exist" check.
- Removal is unaffected: `remove` of an absent quad is a no-op today and should
  stay one.
- Worth deciding at the same time whether longer cycles (A hasPart B, B hasPart
  C, C hasPart A) are in scope. The two-work case is cheap and complete; the
  general case needs a graph walk and may not be worth it.

## Repro

```
cd ~/libcat-e2e && node ui/probe_relations.mjs
```

Expect `R6` and `R6b` to flip to PASS, with `R1`-`R5`, `R7` and `R8` still
passing. The probe mints its own sentinel works and tombstones them; no
pre-existing record is related to anything. `harness/retest.mjs` carries the same
check as `t232`.

## Note (not filed separately)

`R9` in the same probe: relating a live work to a **tombstoned** one returns 204.
The handler checks that the target's grain exists, and a tombstone leaves the
grain in place, so a retired work can be linked as a part. Whether that should be
refused depends on what the public projection does with a part that is gone --
I did not chase it, and it is a separate question from the contradiction above.

## Outcome

Shipped in **v0.82.0**. The guard generalizes past the reported pair: the
handler refuses any add that would close a **containment cycle**, of which
"A contains B and A is a part of B" is the depth-1 case.

### The shape

`bf:hasPart` and `bf:partOf` are inverses and every link writes its inverse on
the target, so the containment graph is fully readable off `hasPart` edges
alone -- `A partOf B` is stored as `B hasPart A` on B's grain. Adding "whole
contains part" therefore closes a cycle exactly when *whole* is already
reachable from *part* by walking `hasPart` down. That single walk subsumes
both the reported contradiction and the self-relation the handler already
refused.

- `backend/httpapi/relations_handlers.go` -- `containmentCycle(ctx, bs, whole,
  part)`: BFS down `hasPart` from *part* looking for *whole*, visited set,
  capped at `relationWalkLimit = 512` grains. It runs **before either write**,
  alongside the existing both-grains-must-exist check, so no half-link is left.
  A target whose grain has vanished ends its branch rather than failing the
  write: a dangling link is not a cycle.
- `bibframe/relations.go` -- `SetWorkRelation` independently refuses an add
  when the grain already asserts the inverse predicate to the same target
  (the 202/211/214 both-layers pattern). The handler catches the pair first
  with a 400; this keeps the contradiction out of any grain regardless of
  caller, and surfaces as a 409 through `writeMutateError`.
- Removal is untouched: `remove` of an absent quad is still a no-op, asserted
  at both layers.

The refusal message names the existing link -- `400 "would create a containment
cycle: <part> already contains <whole>"` -- and `RelationsPanel.svelte` already
renders the server's message through `humanApiMessage`, so no UI change was
needed.

### Longer cycles: in scope

The task left this open ("may not be worth it"). It turned out to be free: the
two-work case *is* the walk at depth 1, so refusing only the pair would have
meant writing the same traversal and then truncating it. The cap is the only
concession -- a hand-built containment tree (a set, its volumes, their parts)
is orders of magnitude under 512, and the cap exists to stop a pathological
corpus from turning one link into an unbounded read, not to bound correctness.
A diamond (two routes to the same part) is **not** a cycle and stays allowed;
`TestWorkRelationCycles` pins that alongside the three-work cycle, the
idempotent re-add, and the absent-link removal.

### Verification

- `TestWorkRelations` (bibframe) and `TestWorkRelationsAPI` +
  `TestWorkRelationCycles` (httpapi); full `go test ./...` green in both
  modules.
- The filer's `ui/probe_relations.mjs` against the rebuilt 8481: **11/12**,
  with `R6` and `R6b` flipped (`R6 -> 400 "would create a containment cycle:
  … already contains …"`) and `R1`-`R5`, `R7`, `R8` still passing.
- `harness/retest.mjs`: **232 FIXED, STILL-BROKEN: none** across all 22 checks.

### R9 (the tombstone note): no change, and it is already safe

The filer's open question -- "whether that should be refused depends on what
the public projection does with a part that is gone" -- has an answer already
in the code: `project.go:568` drops a tombstoned work from the projection
entirely, and `resolveRelations` (tasks/222) drops relation targets absent
from the projection. **The OPAC never renders a dead part.**

So refusing the link buys no invariant: a work can always be tombstoned
*after* being linked, and that ordering is unpreventable, which is exactly why
the projection-side guard is the real defense and was written first. The
residue is cosmetic -- the librarian's panel lists a retired target -- and the
useful affordance there is *marking* it, not refusing the link. Left unfiled,
as the filer left it; noted in the report so they can decide with the
projection behavior in hand. `R9` remains a deliberate FAIL in their probe.

## Verification (filer)

Fixed. Confirmed 2026-07-09 by `harness/retest.mjs` (`t232`), twice in a row:

```
FIXED  232  relations refuse a contradictory pair
       partOf over an existing hasPart -> 400 "would create a containment cycle:
       wb9382b2a3g4hc already contains w4dlq3q4pau5o6"; A keeps only hasPart
```

The error names the existing link, which is what the filing asked for -- a
cataloger can act on it without going hunting. `t232` asserts both halves: the
second call is refused **and** A keeps only `hasPart`, so a guard that rejected
the request after writing a partial statement would still fail. It stays in the
harness.

Agreed on `R9` (relating to a tombstoned work): marking beats refusing, and the
call belongs with whoever decides the projection behavior. Leaving it as a
documented FAIL in `ui/probe_relations.mjs` rather than a filing.
