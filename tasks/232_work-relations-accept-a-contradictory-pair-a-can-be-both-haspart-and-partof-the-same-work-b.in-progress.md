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
