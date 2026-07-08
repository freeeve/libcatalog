// Package workindex maintains an in-memory identity index over the work-grain
// tree: provider keys, clustering keys, barcodes, and work summaries per
// grain. It exists so interactive request paths cost O(1) blob reads instead
// of re-walking the corpus (tasks/106). Freshness is two-layered: reads
// refresh by ETag diff on a short TTL (one List per window; only changed
// grains are re-fetched and re-scanned), and the API's own write paths push
// their writes in synchronously via Apply, so a session always reads its own
// writes. Writers outside the process (or outside httpapi, until tasks/107
// and 109 route them here) become visible within one TTL.
package workindex

import (
	"context"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/freeeve/libcodex/rdf"

	"github.com/freeeve/libcat/bibframe"
	"github.com/freeeve/libcat/identity"
	"github.com/freeeve/libcat/ingest"
	"github.com/freeeve/libcat/storage/blob"
)

// DefaultTTL matches the editing UI's freshness contract established by the
// works-list cache: fresh after a publish, not per keystroke.
const DefaultTTL = 30 * time.Second

// Ref locates one identity signal's owner: the work that carries it and the
// grain path it was scanned from (so callers can exclude same-grain matches).
type Ref struct {
	WorkID string
	Path   string
}

// grainEntry is everything the index keeps for one scanned grain.
type grainEntry struct {
	etag      string
	identity  identity.GrainIdentity
	merges    []identity.Merge
	barcodes  []string
	summaries []ingest.WorkSummary
}

// Index is the shared corpus index. All methods are safe for concurrent use;
// reads that find the index stale pay the refresh inline (ETag-diff List, so
// an unchanged corpus costs zero Gets).
type Index struct {
	bs           blob.Store
	prefix       string
	ttl          time.Duration
	snapshotPath string

	// Change feed (tasks/156): a best-effort accelerator for cross-container
	// read-your-writes. The List-diff refresh above stays the correctness
	// backstop, so any feed error is logged and ignored, never fatal.
	feedPath      string
	feedTTL       time.Duration
	foldThreshold int

	mu          sync.Mutex
	at          time.Time
	epoch       uint64    // fold generation; shared by snapshot and feed
	feedActive  bool      // poll the feed only once a snapshot base exists
	feedAt      time.Time // feed-poll clock
	feedApplied int       // records applied from the current epoch's feed
	feedETag    string    // last-seen feed ETag, for conditional GETs
	grains      map[string]*grainEntry

	// Derived views, rebuilt lazily after any grain change. Rebuild is
	// O(corpus) in memory but does no I/O; per-key incremental maintenance is
	// the next step if profiling ever shows the rebuild on the save path.
	dirty      bool
	byProvider map[string][]Ref
	byCluster  map[string][]Ref
	barcodes   map[string]bool
	summaries  []ingest.WorkSummary
	paths      map[string]string

	// Snapshot prime drift (tasks/162): after a snapshot load, the first
	// reconcile counts how many primed entries the ETag diff re-fetched
	// anyway. refetched near primed means the snapshot was built against a
	// store with a different ETag scheme, so priming bought nothing.
	primePending   bool
	primeEntries   int
	primeRefetched int
}

// New returns an index over the grains under prefix (normally "data/works/").
// The first read pays the full corpus scan; subsequent reads are cache hits.
func New(bs blob.Store, prefix string) *Index {
	return &Index{
		bs: bs, prefix: prefix, ttl: DefaultTTL,
		snapshotPath:  DefaultSnapshotPath,
		feedPath:      DefaultFeedPath,
		feedTTL:       DefaultFeedTTL,
		foldThreshold: DefaultFoldThreshold,
		grains:        map[string]*grainEntry{},
	}
}

// SetSnapshotPath overrides where Save/LoadSnapshot read and write the persisted
// projection (default DefaultSnapshotPath). Call it before first use; the
// offline seed tool uses it to honor an --out flag.
func (ix *Index) SetSnapshotPath(p string) { ix.snapshotPath = p }

// Refresh makes the index fresh now if its TTL has lapsed -- the boot-time
// warmer's entry point; reads call it implicitly.
func (ix *Index) Refresh(ctx context.Context) error {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	return ix.refreshLocked(ctx)
}

// RefreshNow reconciles against a fresh listing regardless of the TTL -- for
// callers about to make corpus-accuracy-sensitive decisions (a copycat
// commit's re-match, tasks/107). Still an ETag diff: only changed grains are
// re-read.
func (ix *Index) RefreshNow(ctx context.Context) error {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	ix.at = time.Time{}
	return ix.refreshLocked(ctx)
}

// Update re-reads the given grain paths and applies their current state --
// how a bulk writer that lands many grains at once (copycat commit/revert)
// keeps the index exact without waiting out the TTL. A path that no longer
// exists is dropped from the index.
func (ix *Index) Update(ctx context.Context, paths ...string) error {
	for _, p := range paths {
		grain, etag, err := ix.bs.Get(ctx, p)
		if errors.Is(err, blob.ErrNotFound) {
			ix.mu.Lock()
			delete(ix.grains, p)
			ix.dirty = true
			ix.mu.Unlock()
			continue
		}
		if err != nil {
			return err
		}
		ix.Apply(p, etag, grain)
	}
	return nil
}

// Apply records a grain the caller just wrote (or re-read), keeping the index
// exact for the process's own writes without waiting out the TTL.
func (ix *Index) Apply(grainPath, etag string, grain []byte) {
	entry, err := scanEntry(etag, grain)
	ix.mu.Lock()
	defer ix.mu.Unlock()
	if err != nil {
		// A grain the store accepted but the scanner cannot parse: drop the
		// stale entry rather than serve it; the next refresh retries.
		delete(ix.grains, grainPath)
	} else {
		ix.grains[grainPath] = entry
	}
	ix.dirty = true
}

// Summaries returns every work's summary, sorted by work id (the same shape
// and order as ingest.ScanSummaries). The slice is shared: read-only.
func (ix *Index) Summaries(ctx context.Context) ([]ingest.WorkSummary, error) {
	summaries, _, err := ix.SummariesWithPaths(ctx)
	return summaries, err
}

// SummariesWithPaths returns every work's summary plus each work's grain
// path -- the ingest.SummarySource contract the worker paths consume
// (tasks/109). Both return values are shared: read-only.
func (ix *Index) SummariesWithPaths(ctx context.Context) ([]ingest.WorkSummary, map[string]string, error) {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	if err := ix.freshenLocked(ctx); err != nil {
		return nil, nil, err
	}
	return ix.summaries, ix.paths, nil
}

// ProviderOwners returns the works whose instances carry the provider key
// (isbn:/issn:/id:-namespaced identifier), ordered by grain path.
func (ix *Index) ProviderOwners(ctx context.Context, key string) ([]Ref, error) {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	if err := ix.freshenLocked(ctx); err != nil {
		return nil, err
	}
	return ix.byProvider[key], nil
}

// ClusterOwners returns the works carrying the author/title/language
// clustering key, ordered by grain path.
func (ix *Index) ClusterOwners(ctx context.Context, key string) ([]Ref, error) {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	if err := ix.freshenLocked(ctx); err != nil {
		return nil, err
	}
	return ix.byCluster[key], nil
}

// DuplicateGroups returns cluster keys shared by two or more distinct works:
// key -> sorted work ids. The map is the caller's to keep.
func (ix *Index) DuplicateGroups(ctx context.Context) (map[string][]string, error) {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	if err := ix.freshenLocked(ctx); err != nil {
		return nil, err
	}
	groups := map[string][]string{}
	for key, refs := range ix.byCluster {
		seen := map[string]bool{}
		for _, ref := range refs {
			seen[ref.WorkID] = true
		}
		if len(seen) < 2 {
			continue
		}
		ids := make([]string, 0, len(seen))
		for id := range seen {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		groups[key] = ids
	}
	return groups, nil
}

// SeedResolver seeds r with every committed work/instance identity and merge
// marker in the corpus, in grain path order -- what copycat's match pass
// needs from LoadPriorStore, without the per-request corpus read (tasks/107).
func (ix *Index) SeedResolver(ctx context.Context, r *identity.Resolver) error {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	if err := ix.freshenLocked(ctx); err != nil {
		return err
	}
	paths := make([]string, 0, len(ix.grains))
	for p := range ix.grains {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		identity.SeedResolver(r, []identity.GrainIdentity{ix.grains[p].identity})
	}
	for _, p := range paths {
		for _, m := range ix.grains[p].merges {
			r.SeedMerge(m.From, m.To)
		}
	}
	return nil
}

// GrainPaths returns the set of grain paths the index currently covers. The
// map is a copy the caller may keep.
func (ix *Index) GrainPaths(ctx context.Context) (map[string]bool, error) {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	if err := ix.refreshLocked(ctx); err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(ix.grains))
	for p := range ix.grains {
		out[p] = true
	}
	return out, nil
}

// Barcodes returns every barcode in the corpus. The map is a copy the caller
// may mutate (the bulk-add generator reserves candidates in it).
func (ix *Index) Barcodes(ctx context.Context) (map[string]bool, error) {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	if err := ix.freshenLocked(ctx); err != nil {
		return nil, err
	}
	taken := make(map[string]bool, len(ix.barcodes))
	for bc := range ix.barcodes {
		taken[bc] = true
	}
	return taken, nil
}

// freshenLocked refreshes on TTL lapse and rebuilds the derived views if any
// grain changed.
func (ix *Index) freshenLocked(ctx context.Context) error {
	// The feed is the fast path for reads-your-writes; failures fall through to
	// the List-diff refresh, which is the correctness backstop.
	ix.pollFeedLocked(ctx)
	if err := ix.refreshLocked(ctx); err != nil {
		return err
	}
	ix.rebuildLocked()
	return nil
}

// refreshLocked reconciles the per-grain entries against a fresh listing:
// unchanged ETags keep their scan, changed and new grains are re-fetched,
// unlisted grains are dropped.
func (ix *Index) refreshLocked(ctx context.Context) error {
	if !ix.at.IsZero() && time.Since(ix.at) < ix.ttl {
		return nil
	}
	seen := map[string]bool{}
	refetched := 0
	for entry, err := range ix.bs.List(ctx, ix.prefix) {
		if err != nil {
			return err
		}
		if !isGrainPath(entry.Path) {
			continue
		}
		seen[entry.Path] = true
		if cur, ok := ix.grains[entry.Path]; ok && cur.etag == entry.ETag {
			continue
		} else if ok && ix.primePending {
			refetched++
		}
		grain, etag, err := ix.bs.Get(ctx, entry.Path)
		if errors.Is(err, blob.ErrNotFound) {
			// Deleted between List and Get; the unlisted-path sweep below
			// would miss it because List already yielded it.
			delete(seen, entry.Path)
			continue
		}
		if err != nil {
			return err
		}
		scanned, err := scanEntry(etag, grain)
		if err != nil {
			return fmt.Errorf("workindex: %s: %w", entry.Path, err)
		}
		ix.grains[entry.Path] = scanned
		ix.dirty = true
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

// isGrainPath reports whether a listed path is a work grain: an .nq file that
// is not the assembled catalog.nq.
func isGrainPath(p string) bool {
	base := path.Base(p)
	return strings.HasSuffix(base, ".nq") && base != "catalog.nq"
}

// rebuildLocked rederives the lookup views from the grain entries, in grain
// path order so lookups are deterministic.
func (ix *Index) rebuildLocked() {
	if !ix.dirty && ix.byProvider != nil {
		return
	}
	paths := make([]string, 0, len(ix.grains))
	for p := range ix.grains {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	byProvider := map[string][]Ref{}
	byCluster := map[string][]Ref{}
	barcodes := map[string]bool{}
	summaries := make([]ingest.WorkSummary, 0, len(paths))
	workPaths := make(map[string]string, len(paths))
	for _, p := range paths {
		entry := ix.grains[p]
		for _, inst := range entry.identity.Instances {
			if inst.WorkID == "" {
				continue
			}
			for _, pk := range inst.ProviderKeys {
				byProvider[pk] = append(byProvider[pk], Ref{WorkID: inst.WorkID, Path: p})
			}
		}
		for _, wk := range entry.identity.Works {
			if wk.ClusterKey == "" {
				continue
			}
			byCluster[wk.ClusterKey] = append(byCluster[wk.ClusterKey], Ref{WorkID: wk.WorkID, Path: p})
		}
		for _, bc := range entry.barcodes {
			barcodes[bc] = true
		}
		for _, s := range entry.summaries {
			workPaths[s.WorkID] = p
		}
		summaries = append(summaries, entry.summaries...)
	}
	sort.Slice(summaries, func(i, j int) bool { return summaries[i].WorkID < summaries[j].WorkID })
	ix.byProvider, ix.byCluster, ix.barcodes, ix.summaries, ix.paths = byProvider, byCluster, barcodes, summaries, workPaths
	ix.dirty = false
}

// scanEntry extracts everything the index keeps from one grain off a single
// parse: identity signals, work summaries, and item barcodes.
func scanEntry(etag string, grain []byte) (*grainEntry, error) {
	ds, err := rdf.ParseNQuads(grain)
	if err != nil {
		return nil, err
	}
	entry := &grainEntry{
		etag:      etag,
		identity:  identity.ScanDataset(ds),
		merges:    bibframe.ScanMergesDataset(ds),
		summaries: ingest.SummarizeDataset(ds),
	}
	for _, q := range ds.Quads {
		if q.P.Value == bibframe.PredBarcode && q.O.IsLiteral() {
			entry.barcodes = append(entry.barcodes, q.O.Value)
		}
	}
	return entry, nil
}
