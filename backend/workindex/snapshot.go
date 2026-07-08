// Snapshot persistence for the work index (tasks/155): a cold start loads the
// projection from one blob instead of GETting every grain. The snapshot is the
// in-memory projection (identity + merges + barcodes + summaries per grain),
// serialized as gzipped JSON -- portable, inspectable, and readable by the
// Rust/WASM side, unlike a Go-only gob stream. It is a disposable cache: a
// missing, corrupt, or wrong-version snapshot costs a full scan on the next
// boot, never correctness, because refreshLocked's ETag diff always reconciles.
package workindex

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/freeeve/libcat/identity"
	"github.com/freeeve/libcat/ingest"
	"github.com/freeeve/libcat/storage/blob"
)

// DefaultSnapshotPath is where the persisted projection lives -- outside the
// grain prefix the index lists, so refreshLocked never mistakes it for a grain.
const DefaultSnapshotPath = "data/workindex.snapshot"

// snapshotVersion is the on-disk schema version; a mismatch falls back to a full
// scan rather than risk decoding an incompatible layout.
const snapshotVersion = 1

// snapshotEntry is one grain's projected state, the JSON-portable mirror of the
// unexported grainEntry. Shared shape with the change feed (tasks/156).
type snapshotEntry struct {
	Path      string                 `json:"path"`
	ETag      string                 `json:"etag"`
	Identity  identity.GrainIdentity `json:"identity"`
	Merges    []identity.Merge       `json:"merges,omitempty"`
	Barcodes  []string               `json:"barcodes,omitempty"`
	Summaries []ingest.WorkSummary   `json:"summaries,omitempty"`
}

// snapshotFile is the whole persisted projection: a version tag, the fold epoch
// it was taken at (shared with the change feed, tasks/156), and the grain
// entries in path order.
type snapshotFile struct {
	Version int             `json:"version"`
	Epoch   uint64          `json:"epoch,omitempty"`
	Entries []snapshotEntry `json:"entries"`
}

// entryToSnapshot projects one in-memory grain entry to its serializable form.
func entryToSnapshot(path string, e *grainEntry) snapshotEntry {
	return snapshotEntry{
		Path:      path,
		ETag:      e.etag,
		Identity:  e.identity,
		Merges:    e.merges,
		Barcodes:  e.barcodes,
		Summaries: e.summaries,
	}
}

// buildSnapshotLocked captures the current projection into a serializable file,
// entries in path order for determinism. The caller holds ix.mu.
func (ix *Index) buildSnapshotLocked() snapshotFile {
	paths := make([]string, 0, len(ix.grains))
	for p := range ix.grains {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	file := snapshotFile{Version: snapshotVersion, Epoch: ix.epoch, Entries: make([]snapshotEntry, 0, len(paths))}
	for _, p := range paths {
		file.Entries = append(file.Entries, entryToSnapshot(p, ix.grains[p]))
	}
	return file
}

// Save serializes the current projection to the snapshot blob (gzipped JSON). It
// reads straight from memory -- no grain reads -- so it is cheap to call after a
// warm-up or a publish. Entries are written in path order so the artifact is
// deterministic.
func (ix *Index) Save(ctx context.Context) error {
	ix.mu.Lock()
	file := ix.buildSnapshotLocked()
	ix.mu.Unlock()

	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	if err := json.NewEncoder(gz).Encode(file); err != nil {
		return fmt.Errorf("workindex: encode snapshot: %w", err)
	}
	if err := gz.Close(); err != nil {
		return fmt.Errorf("workindex: gzip snapshot: %w", err)
	}
	if _, err := ix.bs.Put(ctx, ix.snapshotPath, buf.Bytes(), blob.PutOptions{ContentType: "application/gzip"}); err != nil {
		return fmt.Errorf("workindex: put snapshot: %w", err)
	}
	ix.mu.Lock()
	ix.feedActive = true
	ix.mu.Unlock()
	return nil
}

// LoadSnapshot primes the grain entries from the snapshot blob so a cold start
// skips the corpus scan. It leaves the refresh clock unset, so the next read
// still runs the ETag-diff reconcile -- re-reading only grains changed since the
// snapshot and dropping any deleted since. A missing snapshot is not an error
// (first boot). A corrupt or wrong-version one returns an error so the caller
// can log and fall back to the full scan.
func (ix *Index) LoadSnapshot(ctx context.Context) error {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	return ix.loadSnapshotLocked(ctx)
}

// SnapshotDrift reports how the first reconcile after a snapshot load treated
// the primed entries: primed is the entry count the snapshot supplied,
// refetched how many of those the ETag diff re-read anyway. refetched near
// primed means the snapshot was built against a store with a different ETag
// scheme (a dir-built seed copied into S3, tasks/162) -- correctness holds,
// but the boot degrades to the full corpus scan the snapshot was meant to
// avoid. Both are zero until a load-then-reconcile cycle completes.
func (ix *Index) SnapshotDrift() (primed, refetched int) {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	if ix.primePending {
		return 0, 0
	}
	return ix.primeEntries, ix.primeRefetched
}

// WarmScan reconciles against a fresh listing like RefreshNow, but fetches
// changed grains with workers concurrent GETs and reports progress after each
// -- the offline seed tool's path (tasks/162), where a sequential reconcile
// over a high-latency store takes hours. Not for concurrent use with writers:
// a grain written between the List and the final merge may be recorded stale
// until the next refresh. progress may be nil.
func (ix *Index) WarmScan(ctx context.Context, workers int, progress func(fetched, total int)) error {
	if workers < 1 {
		workers = 1
	}
	ix.mu.Lock()
	known := make(map[string]string, len(ix.grains))
	for p, e := range ix.grains {
		known[p] = e.etag
	}
	primePending := ix.primePending
	ix.mu.Unlock()

	seen := map[string]bool{}
	var stale []string
	refetched := 0
	for entry, err := range ix.bs.List(ctx, ix.prefix) {
		if err != nil {
			return err
		}
		if !isGrainPath(entry.Path) {
			continue
		}
		seen[entry.Path] = true
		cur, ok := known[entry.Path]
		if ok && cur == entry.ETag {
			continue
		}
		if ok && primePending {
			refetched++
		}
		stale = append(stale, entry.Path)
	}

	var (
		resMu    sync.Mutex
		firstErr error
		fetched  = make(map[string]*grainEntry, len(stale))
		gone     = map[string]bool{}
		done     int
	)
	sem := make(chan struct{}, workers)
	var wg sync.WaitGroup
	for _, p := range stale {
		resMu.Lock()
		failed := firstErr != nil
		resMu.Unlock()
		if failed {
			break
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(p string) {
			defer wg.Done()
			defer func() { <-sem }()
			grain, etag, err := ix.bs.Get(ctx, p)
			var scanned *grainEntry
			notFound := errors.Is(err, blob.ErrNotFound)
			if err == nil {
				if scanned, err = scanEntry(etag, grain); err != nil {
					err = fmt.Errorf("workindex: %s: %w", p, err)
				}
			}
			resMu.Lock()
			defer resMu.Unlock()
			switch {
			case notFound:
				gone[p] = true // deleted between List and Get
			case err != nil:
				if firstErr == nil {
					firstErr = err
				}
				return
			default:
				fetched[p] = scanned
			}
			done++
			if progress != nil {
				progress(done, len(stale))
			}
		}(p)
	}
	wg.Wait()
	if firstErr != nil {
		return firstErr
	}

	ix.mu.Lock()
	defer ix.mu.Unlock()
	for p, e := range fetched {
		ix.grains[p] = e
		ix.dirty = true
	}
	for p := range gone {
		delete(seen, p)
	}
	for p := range ix.grains {
		if !seen[p] {
			delete(ix.grains, p)
			ix.dirty = true
		}
	}
	if ix.primePending {
		ix.primePending = false
		ix.primeRefetched = refetched
	}
	ix.at = time.Now()
	return nil
}

// loadSnapshotLocked is LoadSnapshot with ix.mu held -- the fold-reload path
// (pollFeedLocked) reuses it when a reader sees the epoch advance.
func (ix *Index) loadSnapshotLocked(ctx context.Context) error {
	data, _, err := ix.bs.Get(ctx, ix.snapshotPath)
	if errors.Is(err, blob.ErrNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("workindex: get snapshot: %w", err)
	}
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("workindex: open snapshot gzip: %w", err)
	}
	defer gz.Close()
	var file snapshotFile
	if err := json.NewDecoder(gz).Decode(&file); err != nil {
		return fmt.Errorf("workindex: decode snapshot: %w", err)
	}
	if file.Version != snapshotVersion {
		return fmt.Errorf("workindex: snapshot version %d, want %d", file.Version, snapshotVersion)
	}
	ix.grains = make(map[string]*grainEntry, len(file.Entries))
	for _, e := range file.Entries {
		ix.grains[e.Path] = &grainEntry{
			etag:      e.ETag,
			identity:  e.Identity,
			merges:    e.Merges,
			barcodes:  e.Barcodes,
			summaries: e.Summaries,
		}
	}
	ix.dirty = true
	ix.at = time.Time{} // force the next refresh to reconcile the delta
	// Arm the drift counter (tasks/162): the next reconcile measures how many
	// primed entries its ETag diff re-fetches anyway.
	ix.primePending = true
	ix.primeEntries = len(file.Entries)
	ix.primeRefetched = 0
	// Align to the snapshot's fold generation; the feed carries the delta on top.
	ix.epoch = file.Epoch
	ix.feedApplied = 0
	ix.feedETag = ""
	ix.feedActive = true
	return nil
}
