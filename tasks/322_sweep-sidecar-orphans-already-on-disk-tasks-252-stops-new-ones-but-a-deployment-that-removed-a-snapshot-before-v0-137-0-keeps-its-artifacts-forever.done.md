# 322 -- sweep sidecar orphans already on disk -- tasks/252 stops new ones but a deployment that removed a snapshot before v0.137.0 keeps its artifacts forever

Opened 2026-07-10.

`RemoveSnapshot` now deletes the sidecar artifacts it built (tasks/252, v0.137.0).
That is a fix going forward and nothing more: every artifact set orphaned by a
removal *before* that release is still on disk, and nothing will ever collect it.

The playground is carrying one right now, and it is not the one libcat-e2e reported:

```
$ cat site/data/authorities/sidecar/zze2e.manifest.json
{"version":2,"scheme":"zze2e","source":"data/authorities/vocab/zz-e2e-snap-dzgc.nq",…}

$ ls site/data/authorities/vocab/zz-e2e-snap-dzgc.nq
No such file
```

The report named `zz-e2e-snap-4ryz.nq`, and the harness cleaned that one up. `dzgc`
appeared afterwards, from a later harness cycle. So these accumulate at whatever rate
snapshots are removed, and an operator has no way to tell an orphan from a live
sidecar without reading each manifest and stat-ing the snapshot it names. Left in
place deliberately, as a repro.

## What an orphan looks like

A `sidecar/<scheme>.manifest.json` whose `source` path does not exist in the blob
store. `vocab.Load` pass 3 already computes exactly this: it declines to arm a scheme
whose source is absent from the pass-2 `.nq` inventory, and logs

```
vocab: sidecar stale; serving scheme from resident maps
```

so the detection is written and the line is already emitted at every boot. What is
missing is anything that acts on it.

## Why not just delete them in pass 3

Because that makes a read path destructive, and a transient blob-store error that
hides a `.nq` would then delete a 169MB index that was fine. Pass 3's defensiveness is
what kept tasks/252 a leak rather than a data-loss bug -- that was libcat-e2e's
analysis and it is right. Detect there, delete elsewhere.

## Options

1. **An `lcat` subcommand** -- `lcat vocab gc --dir <site>`: list orphans, `-n` to dry
   run, delete via `vocab.RemoveSidecar` (already exported by tasks/252). Explicit,
   offline, no server needed.
2. **An admin route** -- `POST /v1/vocabsources/gc`, librarian-gated, returning what
   it removed. Discoverable from the UI that created the mess.
3. **A boot-time sweep** behind an env flag, off by default.

(1) is the smallest thing that solves the reported problem and composes with the
`site/` layout the operator already has in hand. But an orphan *count* is cheap --
pass 3 already knows -- and surfacing it on the existing vocabsources view without
deleting anything may be the better first step: tell the operator, then let them
sweep.

## Not urgent

Eight files and 32KB on the playground. It matters at `lcsh` scale (169MB), on an
object-store bill, and because `sidecar/` is the first place anyone debugging
vocabulary loading looks -- where a dangling manifest actively lies about what is
installed.

## Outcome

Took option 1, the offline `lcat` subcommand, shipped in **v0.139.0** (`07cb1e4`).
The detection half is exported so a future admin route or boot-time surface (options
2 and 3) can reuse it rather than re-deriving staleness.

**`vocab.OrphanSidecars(ctx, st, prefix) ([]OrphanSidecar, error)`** -- the read half.
A scheme is an orphan when the snapshot its manifest names is **definitively**
not-found, or when the manifest no longer parses (it arms nothing, and its scheme is
recovered from the file name so it can still be collected).

**`lcat vocab-gc --store <root> [--prefix data/authorities/] [--reap] [--json]`** --
reports by default, deletes with `--reap`, exactly the shape of the `covers` reaper.
I did **not** use the `-n` dry-run flag the option sketch named: report-by-default is
the safer contract (you opt *into* deletion, you don't opt out of it), and it matches
the reaper the operator already knows. Same reason `covers` chose it.

### The discipline the leak's own analysis demanded is enforced and tested

> a transient blob-store error that hides a `.nq` would then delete a 169MB index
> that was fine.

`OrphanSidecars` condemns a sidecar only on `blob.ErrNotFound` from the snapshot read;
any other error fails the scan. `TestOrphanSidecarsSpotsAMissingSourceNotATransient
Error` injects a store that errors on the snapshot read and asserts the scan surfaces
it rather than reporting an orphan. Mutation-checked: relaxing the check to `err != nil`
makes that test fail.

### It does not touch a registry orphan whose sidecar is live

The tasks/255 orphan -- a scheme installed with no source record -- still *serves*, and
its snapshot is present, so `vocab-gc` correctly leaves it alone. Verified on the
playground: `homosaurus` (a registry orphan from 255) was not swept; only `zze2e` (a
dead sidecar) was. Two different orphans, and the sweep distinguishes them.

### Tests, all mutation-checked

- `TestOrphanSidecarsFindsWhatNoSnapshotBacks` -- live sidecar is not an orphan
  (control), a removed snapshot makes it one, and the sweep leaves the store clean.
- `TestOrphanSidecarsCollectsAnUnreadableManifest` -- a bad manifest is collected with
  a filename-derived scheme; a live neighbour is not swept in with it (control).
- `TestOrphanSidecarsSpotsAMissingSourceNotATransientError` -- the discipline above.
- `TestVocabGCReapsOrphansAndSparesLiveSidecars` -- the command end to end over a real
  dir store: orphan gone, live intact.
- `TestVocabGCReportsWithoutReaping` -- the safe default touches nothing.

### Verified on the playground -- and it cleaned up the repro this task left behind

```
$ lcat vocab-gc --store ~/libcat-playground/site
  zze2e  (the snapshot the manifest names is gone, named data/authorities/vocab/zz-e2e-snap-dzgc.nq)
1 orphaned sidecar
re-run with --reap to delete them

$ lcat vocab-gc --store ~/libcat-playground/site --reap
1 deleted sidecar
$ lcat vocab-gc --store ~/libcat-playground/site
no orphan sidecars: every manifest names a snapshot that exists
```

The four real schemes (`homosaurus`, `lcgft`, `lcsh`, `lcshac`) kept all eight
artifacts each. The `zze2e` orphan I had left in place as this task's repro is now
collected, so the playground `sidecar/` is honest again.

### Scope

Detection keys on the manifest, which is what arms a scheme, so a *partial* leftover
set with no manifest (a half-completed removal from some older bug) is not detected.
None exist -- every set BuildSidecar writes has a manifest, and RemoveSidecar takes it
first -- but if one is ever found by hand, `RemoveSidecar(scheme)` collects it.
