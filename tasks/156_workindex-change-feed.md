# 156 -- Workindex change feed: cross-container read-your-writes without a scan

Plane 1 freshness layer of [154]. Depends on [155] (snapshot baseline).

## Problem

"Update several records, then read those updates in admin" must hold on Lambda,
where there are N containers. The writer's in-memory `Apply`
(`backend/workindex/workindex.go`) only updates its own container; a read on
another container never sees the write. Reconciling another container against
the store via `List`+ETag-diff is O(corpus/1000) `List` calls per refresh --
fine at 48k, a wall at millions, and still a per-read cost.

## Fix: an append-only change feed of projected entries

The snapshot ([155]) is the periodic checkpoint; the feed is the tail of changes
since it. Both carry the **projection**, so neither needs a grain rescan.

1. **Append on write.** A publish/commit PUTs its grain(s) then appends their
   projected entries (`{path, etag, entry}`, plus tombstones for deletes) to the
   feed, durably, before returning. A multi-grain op batches into one append.
2. **Replay on read.** A container serves reads from `snapshot + feed replay`.
   The feed is small and bounded; fetch it with a conditional GET (304 when
   unchanged) on a short TTL, or per-read for strict read-your-writes.
3. **Fold in memory.** When the feed crosses a size threshold, regenerate the
   snapshot from `snapshot + feed` in memory (no grain reads) and truncate the
   feed, under ETag-CAS. Keeps the feed bounded and cold-start cheap.

Read-your-writes argument: C1 writes grain A and appends A to the feed durably
before its 200; a later read on C2 fetches the latest feed, replays A over its
snapshot, and sees A -- independent of which container wrote it. Freshness cost
per read is one conditional GET of a bounded object -- O(1), not O(corpus).

## Substrate (swap behind one interface)

- **Now / low write-concurrency:** the feed is a small S3 object appended under
  ETag-CAS (read-modify-write with `If-Match`, retry on conflict). Pure
  `blob.Store`; no new service. Fine for queerbooks.
- **High write-concurrency later:** DynamoDB -- `PutItem` per change (no CAS
  contention), `Query` since-seq, strongly consistent. This is where the
  DynamoDB option belongs: the change feed, not the whole index.

Keep the periodic full `List`+ETag reconcile as a rare backstop for out-of-band
changes (writes not routed through the feed, e.g. the public plane); it is no
longer on the hot path.

## Consumer note

The same feed is the event source for the public plane's incremental rebuild
(task 159) -- one change feed, two consumers.

## Verify

- Two-container simulation: write on A, read on B sees it without a corpus
  `List`.
- Feed fold keeps cold-start load O(snapshot) and steady-state reads O(feed).
- Concurrent appends resolve via ETag-CAS with no lost entries.
