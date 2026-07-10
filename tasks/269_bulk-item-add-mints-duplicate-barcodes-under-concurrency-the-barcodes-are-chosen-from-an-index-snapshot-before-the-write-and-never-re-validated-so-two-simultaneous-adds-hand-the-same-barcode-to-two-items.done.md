# 269 -- bulk item add mints duplicate barcodes under concurrency: the barcodes are chosen from an index snapshot before the write and never re-validated, so two simultaneous adds hand the same barcode to two items

Filed from libcat-e2e on 2026-07-09 (cross-repo ask).

`registerItemsBulk`'s doc comment (`items_bulk.go:25-27`) promises *"N copies in one
action with an auto-incrementing, **collision-checked** barcode pattern"*, and
`nextBarcodes` (`:134-135`) says it starts *"past the highest existing counter for the
prefix and **skipping collisions**"*. Both are true of one request in isolation. Two at
once produce identical barcodes.

Measured against **committed HEAD `dfe8bcc`** on a throwaway clone (`:8470`, never :8481
or :8501).

## Symptom

```
control: two SEQUENTIAL adds to one work
  -> 200/200; persisted 6 barcodes, 0 duplicates
     [zzseq0001 zzseq0002 zzseq0003 zzseq0004 zzseq0005 zzseq0006]

two CONCURRENT adds to the SAME work (count=4 each)
  -> 200/200; persisted 8 barcodes, 4 duplicated
     [zzsam0001 zzsam0002 zzsam0003 zzsam0004]

two CONCURRENT adds to DIFFERENT works (count=4 each)
  -> 200/200; 8 barcodes across the two works, 4 duplicated
     [zzx0001 zzx0002 zzx0003 zzx0004]
```

The sequential control is the argument: the same operation, the same prefix, the same
work, run one after the other, allocates `0001…0006` cleanly. The generator and the work
index's refresh are both correct. Only simultaneity breaks them, and both requests answer
`200` -- this is not one request failing, it is two requests succeeding at minting the
same barcode.

In the same-work case the instance ends up holding eight items and four barcodes, each
twice. In the cross-work case **two different works carry the same physical barcode**.

## Root cause

The barcodes are chosen once, before any write, and nothing revisits them.
`backend/httpapi/items_bulk.go:93-116`:

```go
taken, err := ix.Barcodes(r.Context())      // a snapshot (defensively copied)
if err != nil { … }
barcodes := nextBarcodes(taken, req.BarcodePrefix, width, req.Count)
generated := make([]bibframe.Item, req.Count)
for i, bc := range barcodes { generated[i] = bibframe.Item{…, Barcode: bc} }
…
etag, err := mutateWorkGrain(r, bs, ix, workID, func(g []byte) ([]byte, error) {
	current, err := bibframe.ItemsOf(g, req.InstanceID)
	if err != nil { return nil, err }
	return bibframe.SetItems(g, req.InstanceID, append(current, generated...))
})
```

`generated` is captured by the closure. `mutateWorkGrain`
(`records_handlers.go:480-506`) is a compare-and-swap retry loop:

```go
for attempt := range 6 {
	grain, etag, err := bs.Get(r.Context(), path)
	updated, err := mutate(grain)                                   // fresh grain…
	newTag, err := bs.Put(r.Context(), path, updated, blob.PutOptions{IfMatch: etag, …})
	if errors.Is(err, blob.ErrPreconditionFailed) { continue }      // …retry on conflict
	…
}
```

It re-reads the grain and re-runs the closure, which is exactly the right shape for a
concurrent *edit*. It is the wrong shape for a concurrent *allocation*: the closure
recomputes `current` from the fresh grain and then appends the **stale** `generated`. The
CAS loop's whole job is to make a concurrent write safe, and here it faithfully launders
barcodes chosen against a superseded snapshot into the winning grain.

Whichever request writes second carries barcodes chosen before the first request's write
landed -- whether it got there by losing the CAS and retrying, or simply by calling
`ix.Barcodes` before the winner committed and `bs.Get` after. The window is the whole
span from `ix.Barcodes` to `bs.Put`, and `ix.Barcodes` (`workindex.go:279-290`) takes
`ix.mu`, copies the set, and releases it -- it reserves nothing.

Across two different works the CAS never fires at all: two grains, two paths, two
independent `IfMatch` checks, both satisfied. There is no shared object to compare and
swap on, so nothing can detect the collision.

**And nothing downstream catches it.** There is no barcode-uniqueness validation
anywhere -- not in `bibframe`, not in the editor's item ops, not in `httpapi`. Grepping
the tree for a duplicate-barcode guard returns nothing. `nextBarcodes`' collision check
against `taken` is the only uniqueness enforcement libcat has, and it is advisory.

## Why it matters

A barcode is not a record field. It is the label on the physical book, and it is the key
circulation scans to decide which item just went out the door. Two items sharing one is
the failure the barcode exists to prevent.

**libcat has already decided this.** `bibframe/itemops.go:18-20` excludes `barcode` from
the batch-addressable item fields, on precisely this ground:

> Barcode is deliberately absent. A barcode names one physical copy, so **assigning one
> across a selection would mint duplicates**; clearing one across a selection would
> silently unlink the shelf from the catalog.

Batch edit refuses to touch barcodes because it might mint duplicates. Bulk add mints
them. The asymmetry is what makes this a defect rather than a tolerable limitation: the
project has already written down that duplicate barcodes are not an acceptable outcome,
in a comment guarding a *less* likely path than this one.

**The concurrency here is the normal case, not an exotic one.** Bulk add exists for
processing a shipment: a cataloger opens a record and adds the twelve copies that just
arrived. Two catalogers working a delivery together, at two desks, with the library's
standard barcode prefix, is the ordinary way this feature gets used. Neither sees an
error; both see `200` and a tidy list of barcodes. The duplicates are discovered at the
circulation desk, months later, by a patron.

**Recovery is physical.** The barcodes have been printed and stuck to books by the time
anyone notices. Reconciling means finding both items, working out which is which, and
relabelling one -- and the record gives no hint which of the two duplicate rows was the
one whose label got printed first.

**The cross-work case is the dangerous one.** Duplicates within a single instance are at
least visible in one item list, side by side. Two barcodes on two different works look
completely normal from every screen in the application; nothing ever displays them
together. The only way to find them is a full scan of the index, which is precisely what
`ix.Barcodes` does -- once, at the start of a request that is about to create the
problem.

Nothing is corrupted, in the sense that every grain is well-formed. What is broken is the
one invariant this feature exists to maintain.

## Expected

The allocation has to be validated against the grain it is being written into, inside the
critical section that already exists.

- **Recompute inside the closure.** `mutateWorkGrain` already re-runs `mutate` on a fresh
  grain for exactly this reason. Move `ix.Barcodes` + `nextBarcodes` inside it, so a
  retry re-allocates against the state it is actually appending to. That closes the
  same-work case completely and it is a small change: the closure has the request in
  scope.
- **The cross-work case needs a real reservation**, because there is no shared CAS
  object. Options, roughly in order of cost: keep the barcode set in the doc store behind
  a conditional write (`store.CondIfAbsent` per barcode -- `suggest/service.go` already
  uses this pattern for its status index); or serialize allocation behind a mutex in the
  `workindex.Index`, which is already the process-wide owner of `ix.barcodes` and already
  takes `ix.mu` to answer `Barcodes()`. A single-process deployment is the only one
  libcat supports today (`LCATD_LOCAL_SIGNING_KEY`'s warning says as much), so a mutex
  around "read the set, pick N, mark them taken" is sufficient and honest. Say so in a
  comment, so the day libcat scales past one process this is a known seam.
- **Give the invariant a home.** Whatever the allocation does, a duplicate barcode should
  be rejected on write, not merely avoided on generation. Today a cataloger can also type
  one by hand into the item editor and nothing objects, so the generator's carefulness is
  the only thing standing between the catalog and a duplicate. If uniqueness is genuinely
  intended to be advisory, `registerItemsBulk`'s "collision-checked" comment should say
  what it is checked against and what it is not.
- Note that `dryRun` (`:106-109`) previews `generated` without reserving it, so two
  catalogers previewing before committing see the same barcodes and are given no reason
  to doubt them.

Related: the `len(existing)+req.Count > 200` cap (`:89`) is likewise checked against a
grain read before the mutation and never re-checked inside the closure, so two concurrent
adds can carry an instance past 200 items.

## Repro

```bash
cd ~/libcat-e2e && node harness/probe_items_bulk_race.mjs   # R2, R3
cd ~/libcat-e2e && node harness/retest.mjs                  # check t269
```

The probe never addresses :8481 or :8501, and it never reads `~/libcat`'s working tree:
`roinstance.buildHead()` exports committed HEAD with `git archive` into a scratch dir and
builds `cmd/lcatd` there. It clones the playground's site (`cp -Rc`, copy-on-write), boots
a writable instance on :8470, and deletes the clone afterwards -- so the items it creates
need no cleanup and can never reach the playground.

Its controls carry the argument. `R1` runs the two adds **sequentially** and gets six
distinct barcodes, which proves the generator and the index refresh are correct and that
only simultaneity breaks them. `R4a`/`R4b` prove both concurrent requests returned `200`,
so the duplicates are two successes rather than one failure.

By hand, against any instance:

```bash
TOK=…
W=…; I=…      # a work and one of its instance ids (GET /v1/works/$W/doc -> .doc.instances[0].id)
BODY='{"instanceId":"'$I'","count":4,"barcodePrefix":"zzdup","barcodeWidth":4}'

curl -s -XPOST -H "Authorization: Bearer $TOK" -H 'Content-Type: application/json' -d "$BODY" \
  localhost:8470/v1/works/$W/items/bulk &
curl -s -XPOST -H "Authorization: Bearer $TOK" -H 'Content-Type: application/json' -d "$BODY" \
  localhost:8470/v1/works/$W/items/bulk &
wait

curl -s -H "Authorization: Bearer $TOK" localhost:8470/v1/works/$W | jq -r .nquads \
  | grep -o '"zzdup[0-9]*"' | sort | uniq -c | sort -rn | head
#    2 "zzdup0001"
#    2 "zzdup0002"
#    2 "zzdup0003"
#    2 "zzdup0004"
```

Point the two curls at two *different* works to get the cross-work shape, where the CAS
retry never fires and the two records each carry the same four barcodes.

## Outcome

Fixed in **v0.106.0** (`21953a1`). `probe_items_bulk_race.mjs` 5/5 against
committed HEAD; `retest.mjs` **t269 FIXED**, nothing regressed.

The diagnosis was exact, including the part that is easy to get wrong: the CAS
loop is not broken, it is the wrong tool. It re-runs `mutate` on a fresh grain
because that is what a concurrent *edit* needs, and it then faithfully launders
an allocation made against the grain it just discarded.

### Two fixes, because there are two races

**Allocation moved inside the closure.** A retry now re-reads `ix.Barcodes()`,
unions it with the items already on the fresh grain, and re-mints. This closes
the CAS-retry race, and it closes it against *any* concurrent writer, not just
another bulk add -- an item typed into the editor between the snapshot and the
write would have poisoned the allocation the same way.

**The whole span runs under a new allocation lock**, `workindex.AllocateBarcodes`.
The cross-work case has no shared object to compare and swap on, so nothing in
the write path can arbitrate it; serialization is the only thing that makes the
generator's check mean anything. It is a mutex, deliberately not `ix.mu` (the
allocator holds it across a grain read, a choice, and a grain write, and `ix.mu`
is taken and released several times inside that span). Commented as the seam to
replace with a per-barcode conditional write in the shared store the day libcat
runs more than one process.

The layering matters and both layers are separately proven. Removing the lock
leaves `TestBulkAddConcurrentDifferentWorksMintNoDuplicates` failing while the
same-work test still passes. Moving allocation back outside the closure leaves
`TestBulkAddRetryReallocatesAgainstTheFreshGrain` failing while both concurrency
tests still pass. Neither fix subsumes the other, and a suite with only the
obvious concurrency tests would have let the second regress silently.

### The related note was right too

The `len(existing)+req.Count > 200` cap was read before the mutation and never
re-checked, so two concurrent adds of 20 against 180 existing items produced 220.
It is enforced inside the closure now and reported as the same 400. The test
proves exactly one of the two adds fits.

### Smaller things

`dryRun` still previews without reserving, which is correct -- there is nothing to
reserve -- and now says so in a comment. The response reports the barcodes that
were *stored* rather than the ones first chosen, which matters once a retry can
reallocate; `TestBulkAddResponseMatchesWhatWasPersisted` pins it.

### Not done: the constraint

The report's third bullet -- give the invariant a home, reject a duplicate on
write -- is **not** in this release, and `registerItemsBulk`'s doc comment now
says plainly what is checked and what is not. A cataloger can still type a
duplicate into the item editor, and MARC import writes one straight through.
That is a pre-existing gap on a different surface, it needs a decision about
retired items reusing barcodes, and it needs a *report* before a constraint,
since a deployment that ran this racy version may be carrying duplicates today
and a new 409 would fail writes to records that were fine yesterday. Filed as
**tasks/270**.
