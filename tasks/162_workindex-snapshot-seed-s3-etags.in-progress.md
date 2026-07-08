# 162 -- workindex-snapshot: dir-built seeds can't prime an S3 store (ETag scheme mismatch)

Filed from the queerbooks-demo session (2026-07-07) while adopting 155/156;
renumbered from 161 (collision with the committed release task).

## Problem

`lcatd workindex-snapshot --blob-dir <dir>` (backend/cmd/lcatd/snapshot.go)
builds the snapshot through `blob.NewDir`, whose ETags are **sha256 of
content** (storage/blob/dir.go). An S3 store's ETags are **MD5-based**. So the
documented Lambda seed flow -- build from a local grain mirror, `aws s3 cp` the
snapshot next to the grains -- produces a snapshot in which *every* entry
fails the ETag-diff on the first refresh against S3. The refresh re-GETs all
48k grains; on Lambda that read blocks past the 30s window and never completes
across frozen invocations, so `/v1/works` still 503s permanently. The "stale
snapshot degrades to a small catch-up" guarantee silently doesn't hold across
store backends: identical bytes, different fingerprint scheme.

queerbooks hit this for real (their tasks/014). Workaround used there: run the
full `lcatd` off-Lambda with `LCATD_S3_BUCKET=<bucket>` and let the boot
goroutine warm-scan + `Save` back into the bucket -- correct ETags, but ~1h of
sequential GETs over a home uplink and no progress signal (reads block behind
the refresh lock, so only network counters show it's alive).

## Suggested fixes (either suffices; first is more useful)

1. Teach the seed tool to talk to the target store directly:
   `lcatd workindex-snapshot --s3-bucket <bucket>` (reuse awsstore.S3 wiring
   from appdeps). Snapshot then carries native S3 ETags and writes straight to
   the bucket. Concurrent GETs would also fix the ~1h sequential scan.
2. And/or make the mismatch loud: log at WARN when >N% of snapshot entries
   fail the ETag diff on first refresh ("snapshot ETag scheme may not match
   this store") so the misuse is diagnosable instead of a silent full rescan.

Docs: the deploy README seed recipe should say the snapshot must be built
against the same store backend it will serve (or use fix 1).
