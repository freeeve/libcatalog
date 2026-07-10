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
