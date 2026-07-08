// Change feed for the work index (tasks/156): a best-effort accelerator that
// gives cross-container read-your-writes without a corpus List. A write appends
// its projected entry (or a tombstone) to a small feed blob; a reader replays
// the feed over its snapshot. The List-diff refresh (workindex.go) stays the
// correctness backstop, so every feed operation here is best-effort -- a failure
// is logged by the caller and ignored, never fatal.
//
// The feed and snapshot share a fold epoch. When the feed would grow past the
// fold threshold (e.g. a bulk update), the writer folds instead: it saves a
// fresh snapshot at epoch+1 and resets the feed to empty, so the feed stays
// bounded and readers reload the new base rather than replaying a huge delta.
package workindex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/freeeve/libcat/storage/blob"
)

// DefaultFeedPath is where the change feed lives -- outside the grain prefix the
// index lists, like the snapshot.
const DefaultFeedPath = "data/workindex.feed"

// DefaultFeedTTL bounds how often a read re-polls the feed; within it, reads are
// served from memory.
const DefaultFeedTTL = 2 * time.Second

// DefaultFoldThreshold is the feed record count past which an append folds into
// a fresh snapshot instead of growing the feed.
const DefaultFoldThreshold = 4096

const feedVersion = 1

// feedCASRetries bounds the optimistic-concurrency retry loop on the feed blob.
const feedCASRetries = 6

// feedRecord is one change: a projected entry (add/update) or, with Deleted, a
// tombstone carrying only the path.
type feedRecord struct {
	snapshotEntry
	Deleted bool `json:"deleted,omitempty"`
}

// feedFile is the whole feed: a version tag, the fold epoch it belongs to, and
// the records appended since that epoch's snapshot.
type feedFile struct {
	Version int          `json:"version"`
	Epoch   uint64       `json:"epoch,omitempty"`
	Records []feedRecord `json:"records"`
}

// AppendFeed records the current state of the given grain paths (or a tombstone
// for a path no longer present) to the change feed, so other containers see the
// write without a corpus List. Call it after Apply/Update has committed the
// write in memory. Best-effort: callers log the error but do not fail the write,
// since the List-diff refresh is the backstop.
func (ix *Index) AppendFeed(ctx context.Context, paths ...string) error {
	if len(paths) == 0 {
		return nil
	}
	ix.mu.Lock()
	recs := make([]feedRecord, 0, len(paths))
	for _, p := range paths {
		if e, ok := ix.grains[p]; ok {
			recs = append(recs, feedRecord{snapshotEntry: entryToSnapshot(p, e)})
		} else {
			recs = append(recs, feedRecord{snapshotEntry: snapshotEntry{Path: p}, Deleted: true})
		}
	}
	ix.mu.Unlock()
	return ix.appendChanges(ctx, recs)
}

// appendChanges writes records to the feed under optimistic concurrency, folding
// into a fresh snapshot rather than growing the feed past the threshold.
func (ix *Index) appendChanges(ctx context.Context, recs []feedRecord) error {
	if len(recs) == 0 {
		return nil
	}
	for range feedCASRetries {
		file, etag, exists, err := ix.getFeedFile(ctx)
		if err != nil {
			return err
		}
		if !exists {
			ix.mu.Lock()
			file.Epoch = ix.epoch
			ix.mu.Unlock()
		}
		if len(file.Records)+len(recs) > ix.foldThreshold {
			return ix.foldFeed(ctx, file.Epoch, etag, exists)
		}
		file.Version = feedVersion
		file.Records = append(file.Records, recs...)
		newETag, err := ix.putFeedFile(ctx, file, etag, exists)
		if errors.Is(err, blob.ErrPreconditionFailed) {
			continue // a concurrent writer moved the feed; retry
		}
		if err != nil {
			return err
		}
		ix.mu.Lock()
		if file.Epoch == ix.epoch {
			ix.feedApplied = len(file.Records)
			ix.feedETag = newETag
		}
		ix.mu.Unlock()
		return nil
	}
	return fmt.Errorf("workindex: feed append contention after %d attempts", feedCASRetries)
}

// foldFeed collapses the feed into a fresh snapshot at the next epoch: it saves
// the current in-memory projection (already including the just-committed write)
// stamped epoch+1, then resets the feed to empty at epoch+1 under CAS.
func (ix *Index) foldFeed(ctx context.Context, epoch uint64, feedETag string, feedExists bool) error {
	next := epoch + 1
	ix.mu.Lock()
	ix.epoch = next
	ix.mu.Unlock()
	if err := ix.Save(ctx); err != nil { // Save stamps ix.epoch (= next)
		return err
	}
	newETag, err := ix.putFeedFile(ctx, feedFile{Version: feedVersion, Epoch: next}, feedETag, feedExists)
	if err != nil {
		return err
	}
	ix.mu.Lock()
	ix.feedApplied = 0
	ix.feedETag = newETag
	ix.mu.Unlock()
	return nil
}

// pollFeedLocked applies feed records the container has not yet seen, so a write
// on another container becomes visible without a corpus List. Gated by feedTTL,
// best-effort: on any error it returns and leaves the backstop refresh to
// reconcile. The caller holds ix.mu.
func (ix *Index) pollFeedLocked(ctx context.Context) {
	// The feed only augments a snapshot base; an index warming from scratch
	// (no snapshot) reconciles through the List refresh alone.
	if !ix.feedActive {
		return
	}
	if !ix.feedAt.IsZero() && time.Since(ix.feedAt) < ix.feedTTL {
		return
	}
	data, etag, err := ix.bs.Get(ctx, ix.feedPath)
	if err != nil || etag == ix.feedETag {
		// Missing feed, transient error, or unchanged since last poll: nothing
		// to apply.
		ix.feedAt = time.Now()
		return
	}
	var file feedFile
	if err := json.Unmarshal(data, &file); err != nil {
		ix.feedAt = time.Now()
		return
	}
	if file.Epoch > ix.epoch {
		// A fold happened: reload the fresh base, then apply this epoch's feed.
		if err := ix.loadSnapshotLocked(ctx); err != nil {
			return // leave feedAt unset to retry; the backstop still covers reads
		}
	}
	start := ix.feedApplied
	if file.Epoch != ix.epoch || start > len(file.Records) {
		start = 0 // epoch changed or feed reset shorter: reapply from the top
	}
	for _, r := range file.Records[start:] {
		ix.applyFeedRecordLocked(r)
	}
	ix.feedApplied = len(file.Records)
	ix.epoch = file.Epoch
	ix.feedETag = etag
	ix.feedAt = time.Now()
}

// applyFeedRecordLocked applies one record to the in-memory grains,
// idempotently. The caller holds ix.mu.
func (ix *Index) applyFeedRecordLocked(r feedRecord) {
	if r.Deleted {
		if _, ok := ix.grains[r.Path]; ok {
			delete(ix.grains, r.Path)
			ix.dirty = true
		}
		return
	}
	ix.grains[r.Path] = &grainEntry{
		etag:      r.ETag,
		identity:  r.Identity,
		merges:    r.Merges,
		barcodes:  r.Barcodes,
		summaries: r.Summaries,
	}
	ix.dirty = true
}

// getFeedFile reads the feed and its ETag; a missing feed yields an empty file
// (found=false) so callers can create it.
func (ix *Index) getFeedFile(ctx context.Context) (feedFile, string, bool, error) {
	data, etag, err := ix.bs.Get(ctx, ix.feedPath)
	if errors.Is(err, blob.ErrNotFound) {
		return feedFile{Version: feedVersion}, "", false, nil
	}
	if err != nil {
		return feedFile{}, "", false, err
	}
	var file feedFile
	if err := json.Unmarshal(data, &file); err != nil {
		return feedFile{}, "", false, fmt.Errorf("workindex: decode feed: %w", err)
	}
	return file, etag, true, nil
}

// putFeedFile writes the feed under optimistic concurrency: If-Match the prior
// ETag when it existed, If-None-Match when creating it, so a racing writer is
// caught as a precondition failure.
func (ix *Index) putFeedFile(ctx context.Context, file feedFile, etag string, exists bool) (string, error) {
	data, err := json.Marshal(file)
	if err != nil {
		return "", err
	}
	opts := blob.PutOptions{ContentType: "application/json"}
	if exists {
		opts.IfMatch = etag
	} else {
		opts.IfNoneMatch = true
	}
	return ix.bs.Put(ctx, ix.feedPath, data, opts)
}
