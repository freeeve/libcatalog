# 271 -- concurrent relation adds defeat the containment-cycle guard and leave a cycle whose prescribed remedy is refused

Filed from libcat-e2e on 2026-07-09 (cross-repo ask).

Same shape as **269**: a guard reads state, a write follows, and nothing holds the
reservation in between. Different guard, different invariant, different file.

`containmentCycle` runs before either grain is written. Two relation adds fired together
in opposite directions each see a graph without the other's edge, both pass, and both
write their forward statement. The result is the cycle the guard exists to prevent -- and
the remedy the resulting error message prescribes is then refused.

Measured against **committed HEAD `ba0d751`** on a throwaway clone (`:8469`, never :8481
or :8501).

## Symptom

```
control: four relation-free works       A, B, C, D

control: the guard works, SEQUENTIALLY
  POST /v1/works/A/relations {hasPart, B}   -> 204
  POST /v1/works/B/relations {hasPart, A}   -> 400 "would create a containment cycle:
                                                    A already contains B"

the same two adds, fired together
  POST /v1/works/C/relations {hasPart, D}   -> 500 ┐ both report the documented
  POST /v1/works/D/relations {hasPart, C}   -> 500 ┘ half-link error

GET /v1/works/C/relations   hasPart=[D]  partOf=[]
GET /v1/works/D/relations   hasPart=[C]  partOf=[]
                            ^^^^^^^^^^^ the hasPart walk C→D→C closes a cycle

the remedy the 500 prescribes:
  POST /v1/works/C/relations {hasPart, D}   -> 400 "would create a containment cycle:
                                                    D already contains C"

control: recovery still exists
  DELETE /v1/works/C/relations {hasPart, D} -> 204
  DELETE /v1/works/D/relations {hasPart, C} -> 204   (both sides must be unlinked)
```

The sequential control is the argument: the identical pair of requests, run one after the
other, is correctly refused. Only simultaneity defeats the guard, and both requests answer
`500` -- this is not one request quietly succeeding.

## Root cause

**The guard is a time-of-check test.** `backend/httpapi/relations_handlers.go:104-133`:

```go
if add {
	whole, part := workID, req.Target
	if kind.pred == bibframe.PredPartOf { whole, part = req.Target, workID }
	cycle, err := containmentCycle(r.Context(), bs, whole, part)   // reads committed grains
	if err != nil { writeError(w, http.StatusInternalServerError, "grain store unavailable"); return }
	if cycle {
		writeError(w, http.StatusBadRequest, "would create a containment cycle: "+part+" already contains "+whole)
		return
	}
}
if _, err := mutateWorkGrain(r, bs, ix, workID, func(g []byte) ([]byte, error) {
	return bibframe.SetWorkRelation(g, workID, kind.pred, req.Target, add)
}); err != nil { writeMutateError(w, err); return }
if _, err := mutateWorkGrain(r, bs, ix, req.Target, func(g []byte) ([]byte, error) {
	return bibframe.SetWorkRelation(g, req.Target, kind.inverse, workID, add)
}); err != nil {
	// The forward statement is applied; report the asymmetry rather
	// than hide it. Retrying the same call converges (adds are
	// idempotent, removes of absent quads are no-ops).
	writeError(w, http.StatusInternalServerError, "link applied on "+workID+" but the inverse on "+req.Target+" failed; retry to converge")
	return
}
```

`containmentCycle` (`:168`) walks `bf:hasPart` edges off the **committed** grains via
`bs.Get`. Nothing reserves the edge about to be written. The two writes land on two
different grains, so `mutateWorkGrain`'s CAS retry has no shared object to compare and
swap on -- exactly as in **269**, where the same loop faithfully re-applied stale
barcodes.

**The per-grain backstop is what turns a contradiction into a cycle.**
`bibframe/relations.go:73-88` refuses an add when the grain already asserts the inverse
predicate to that target:

```go
opposite := held.PartOf
if pred == PredPartOf { opposite = held.HasPart }
for _, t := range opposite {
	if t == targetID {
		return nil, fmt.Errorf("work %s already asserts the inverse relation to %s", workID, targetID)
	}
}
```

Its doc comment (`:60-65`) says: *"the handler catches the same pair (and longer cycles)
first, this is the backstop that keeps it out of any grain."* Under the race the handler
does **not** catch it first, and the backstop does its job: the two *inverse* writes are
refused, so no single grain ends up holding `A hasPart B` and `A partOf B` together. That
is why both requests return `500` rather than silently succeeding, and it is worth saying
plainly -- the backstop is the reason this is a graph defect and not a corrupt grain.

What the backstop cannot see is the other grain. Both *forward* writes have already
landed:

```
C's grain:  C hasPart D
D's grain:  D hasPart C
```

which is precisely what `containmentCycle`'s own doc comment describes as *"the two-work
contradiction (A hasPart B plus A partOf B) … simply the depth-1 case of the same walk"*,
now expressed across two grains instead of one.

**And the prescribed remedy cannot run.** The `500` says *"retry to converge"*, and the
comment above it asserts *"Retrying the same call converges (adds are idempotent…)"*. By
the time the operator retries, the graph holds the cycle, so the retry reaches
`containmentCycle` first and is refused with `400 "would create a containment cycle"`. The
one instruction the error gives is the one thing that no longer works. (Idempotence is not
the issue: the add never reaches `SetWorkRelation`.)

Two smaller consequences of the same window:

- **Neither request writes an audit entry.** `WriteAudit` runs only after both grains
  succeed (`:138-144`), so two grains changed and `WORK_RELATE` records neither. That is
  the principle **268** settled from the other direction: an entry that changed a record
  must be attributable.
- The 500 path is reachable without any concurrency at all -- an ordinary blob-store
  failure on the target's grain -- and leaves the same one-sided link. There the retry
  *does* converge, because no cycle was created. The concurrency case is the one where the
  advice is wrong.

## Why it matters

A containment cycle is the one thing this subsystem is built to exclude. `tasks/232`
added the guard, the backstop, **and** a walk designed so that *"the whole containment
graph is readable off hasPart edges alone"*. Two catalogers clicking at the same moment
put a cycle into that graph.

**It is a plausible click.** Relating a set to its volumes is collaborative work: two
people dividing a multipart monograph between them, each opening the record they are
holding, each linking it to the other. The UI offers "add relation" from either side, and
the two directions are the natural way two people would each describe the same
relationship. Neither of them is doing anything wrong.

**Both are told the operation failed, and both are wrong.** They see
`500 … retry to converge`. The catalog now says C contains D and D contains C. Retrying,
as instructed, produces `400 would create a containment cycle` -- an error that blames the
cataloger for a cycle they were not trying to create and that the system created for them.
Nothing in either message suggests the fix, which is to `DELETE` **both** links, from both
records.

**Nothing downstream will notice.** `containmentCycle`'s walk is protected against
infinite recursion by its own `seen` set, so nothing hangs; the cycle simply persists.
`GET /v1/works/{id}/relations` lists direct edges only, so each record looks locally
sensible: C shows "has part: D", D shows "has part: C". Nothing in the application ever
displays them side by side, and the only thing that walks the graph is the guard.

To be precise about the blast radius: the walk returns true only when `whole` is reachable
from `part`, so unrelated future links are unaffected -- adding `E hasPart C` still
succeeds. What the cycle permanently refuses is re-asserting either of the two links
between C and D, which is to say: exactly the retry the `500` told the cataloger to
perform. The graph keeps a cycle that only the guard can see, and the guard's only visible
effect is to block the repair it recommends.

Nothing is corrupted: every grain is well-formed and the backstop kept the contradiction
out of both. What is broken is the invariant the guard exists to maintain, plus the one
recovery instruction the failure hands the operator.

## Expected

- **Re-check the cycle where the write happens, not before it.** The check reads grains
  and the write reads grains; only the second one is inside `mutateWorkGrain`'s retry
  loop. The forward mutation's closure can re-run `containmentCycle` (or at minimum the
  depth-1 test) against the grain it is about to write, so a retry re-validates against
  the state it is actually appending to. That closes the single-grain half.
- **The cross-grain half needs a real reservation**, because the two writes touch two
  grains and there is no shared CAS object -- the same conclusion 269 reaches for
  barcodes. Serialising relation mutations behind one mutex is sufficient and honest for
  a single-process deployment, and cheap: these are rare, cataloger-paced writes. Say so
  in a comment so the seam is known if libcat ever scales past one process.
- **Fix the remedy, or fix the message.** If the two writes cannot be made atomic, the
  `500` must not prescribe a retry that the guard will refuse. It should name both sides
  and the actual repair: *"link applied on C but the inverse on D failed; the graph may
  now hold a cycle -- DELETE the link from both records and re-add it once."* Better: on
  inverse failure, compensate the forward write (`SetWorkRelation(..., add=false)`) the
  way `9956600` compensates a failed attachment byte write, and report a clean 500 with
  nothing applied. Then "retry" is true again, and the half-link state stops existing.
- **Audit the partial application.** Two grains changed and no `WORK_RELATE` entry was
  written (compare **268**, where a `changed` flag was added for exactly this).
- Consider whether `SetWorkRelation`'s backstop should also refuse an add whose target
  grain already claims the inverse. It cannot see the other grain today, which is why the
  race resolves to a cycle rather than an error.

## Repro

```bash
cd ~/libcat-e2e && node harness/probe_relations_cycle_race.mjs   # L4, L5
cd ~/libcat-e2e && node harness/retest.mjs                       # check t271
```

The probe never addresses :8481 or :8501, and it never reads `~/libcat`'s working tree:
`roinstance.buildHead()` exports committed HEAD with `git archive` into a scratch dir and
builds `cmd/lcatd` there. It clones the playground's site (`cp -Rc`, copy-on-write), boots
a writable instance on :8469, and deletes the clone afterwards, so the relations it creates
can never reach the playground.

Its controls carry the argument. `L1` runs the identical pair **sequentially** and is
correctly refused with 400, which exonerates the guard and leaves simultaneity as the only
variable. `L2` shows both concurrent requests reported the documented half-link `500`, so
the cycle is not one request quietly succeeding. `L3` shows neither grain asserts the
contradiction, so the `bibframe` backstop held and what follows is a graph defect rather
than a corrupt grain. `L6` shows two `DELETE`s do repair it, which is what makes the
refused retry a usability failure rather than data loss.

By hand:

```bash
TOK=…
C=…; D=…       # two relation-free works

curl -s -XPOST -H "Authorization: Bearer $TOK" -H 'Content-Type: application/json' \
  -d '{"kind":"hasPart","target":"'$D'"}' localhost:8469/v1/works/$C/relations &
curl -s -XPOST -H "Authorization: Bearer $TOK" -H 'Content-Type: application/json' \
  -d '{"kind":"hasPart","target":"'$C'"}' localhost:8469/v1/works/$D/relations &
wait
# {"error":"link applied on … but the inverse on … failed; retry to converge"}   x2

curl -s -H "Authorization: Bearer $TOK" localhost:8469/v1/works/$C/relations
# {"hasPart":[{"workId":"<D>",…}],"partOf":[]}
curl -s -H "Authorization: Bearer $TOK" localhost:8469/v1/works/$D/relations
# {"hasPart":[{"workId":"<C>",…}],"partOf":[]}      <- C→D→C

# the remedy the 500 prescribed:
curl -s -XPOST -H "Authorization: Bearer $TOK" -H 'Content-Type: application/json' \
  -d '{"kind":"hasPart","target":"'$D'"}' localhost:8469/v1/works/$C/relations
# {"error":"would create a containment cycle: <D> already contains <C>"}   400
```

Run the two curls sequentially instead and the second is refused with the cycle error
before anything is written -- the guard is correct, it is simply not held across the write.

## Outcome

Fixed in **v0.107.0** (`416d01c`). `retest.mjs` **t271 FIXED**; nothing regressed.
`probe_relations_cycle_race.mjs` is 5/7, and the two failures are `L2` and `L3`,
which assert the bug's symptom -- see below.

"The guard is correct, it is simply not held across the write" is the whole
diagnosis, and the fix is written from that sentence.

### Three changes

**One lock across the check-then-write span.** Relation edits are rare and
cataloger-paced, so serializing them costs nothing. Commented as the seam to
replace with a store-side reservation the day libcat runs more than one process,
same as tasks/269's barcode lock.

**The guard re-runs inside the forward mutation's closure.** The lock keeps other
relation edits out, but a plain grain write -- `PUT /v1/works/{id}`, a batch patch
-- can add a `hasPart` quad and lose the forward write its CAS. The retry re-runs
the closure precisely because the graph moved, and the guard's earlier answer was
about a graph that no longer exists. This is what makes the answer binding.

**A failed inverse write is compensated, not reported.** The old 500 prescribed a
retry, and the comment above it claimed adds are idempotent. Both were wrong once
the surviving forward edge was itself a containment claim: the retry reached the
cycle guard reading the very edge that request had written, and was refused with
400. Undoing the forward statement leaves nothing applied, so "retry" is honest
again and the half-link state stops existing. When the rollback fails too, the
error names the actual repair -- delete from both records, then re-add once -- and
the change is audited, because a record that changed must be attributable
(tasks/268). A compensated failure changed nothing and is not audited (tasks/249).

### The layers do not subsume one another

Removing the lock leaves the concurrent tests failing while the retry test
passes. Removing the in-closure re-check leaves the retry test failing (500, not
400) while the concurrent tests pass. Removing the compensation leaves both
inverse-failure tests failing while everything else passes. Each was proven by
stubbing it out and watching a named test fail. A suite with only the obvious
concurrency test would have let the other two regress silently -- the same lesson
269 taught.

Worth noting what the mutation of the in-closure re-check reveals: without it the
race still does not produce a cycle, because `SetWorkRelation`'s per-grain
backstop refuses the inverse and the compensation then undoes the forward write.
It produces a **500 instead of a 400**. So the three defences overlap on the
worst outcome and disagree on the status code, which is exactly why all three are
tested separately.

### Declined: the backstop across grains

The last bullet asks whether `SetWorkRelation` should also refuse an add whose
*target* grain claims the inverse. It cannot: `bibframe` is handed one grain and
has no store. Reaching across grains would put a blob read inside a pure quad
transform. The handler is the right place for a corpus-wide invariant, and it now
holds it across the write, which is what the backstop's doc comment always
assumed ("the handler catches the same pair first").

### Verified end to end from libcat-e2e (2026-07-09, HEAD `9911214`)

`probe_relations_cycle_race.mjs` was **5/7 against the fix, and that was the
probe's fault, not the fix's**. `L2` asserted "both concurrent requests answer
500" and `L3` asserted `C.partOf=[]` — the bug's symptom as a passing condition.
A correct serialization was always going to fail them. Rewritten against the
shipped contract, it is **10/10**, and it now presses three things the original
did not:

**The race is run on 5 distinct pairs, not one.** All five answered `204/400`,
and the winning direction varies between rounds (`204/400 400/204 400/204`), so
the lock is arbitrating rather than an ordering happening to hold. A race probe
that passes once has observed the bug not firing, not the bug being fixed.

**The graph is checked against what the responses claimed** — the winner's link
present in both directions, the refused add having written neither side. Without
this, "no cycle" could be earned by dropping both writes.

**The compensation branch is induced against a real filesystem**, by `chmod a-w`
on *only* the target's grain shard (grains are hash-sharded, so the shard is
found by glob, not derived from the id). The forward write lands, the inverse
cannot, and the measurement is: `500`, `X.hasPart=[]`, `Y.partOf=[]` — the
forward statement rolled back, nothing applied. Audit count unchanged across it
(`10 -> 10`), and a successful add audits exactly once (`7 -> 8`). Control: while
Y's shard is read-only, a relation add touching two writable grains still answers
`204`, so the induced failure is the inverse write and nothing else.

That last one is the reason to say this out loud rather than just flip the row.
It is new code, it changed the failure contract, and it is unreachable without an
induced storage failure — the unit tests fake the store, this drives the branch
the operator will actually meet. `t271` in `retest.mjs` now carries all three
checks, so a regression in any one of them reopens this task rather than the one
that happens to be easiest to trip.
