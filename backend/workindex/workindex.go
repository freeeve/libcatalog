// Package workindex maintains an in-memory identity index over the work-grain
// tree: provider keys, clustering keys, barcodes, and work summaries per
// grain. It exists so interactive request paths cost O(1) blob reads instead
// of re-walking the corpus. Freshness is two-layered: reads
// refresh by ETag diff on a short TTL (one List per window; only changed
// grains are re-fetched and re-scanned), and the API's own write paths push
// their writes in synchronously via Apply, so a session always reads its own
// writes. Writers outside the process (or outside httpapi) become visible
// within one TTL.
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
	etag     string
	identity identity.GrainIdentity
	merges   []identity.Merge
	barcodes []string
	items    []itemBarcode
	// hidden is true when the grain's Work is suppressed or tombstoned; its
	// barcodes are then not "live" for uniqueness.
	hidden    bool
	summaries []ingest.WorkSummary
}

// itemBarcode is one item's barcode and the instance it hangs off, so barcode
// uniqueness can tell a re-save of an instance's own items from a collision with
// a different instance.
type itemBarcode struct {
	barcode    string
	instanceID string
}

// Index is the shared corpus index. All methods are safe for concurrent use;
// reads that find the index stale pay the refresh inline (ETag-diff List, so
// an unchanged corpus costs zero Gets).
type Index struct {
	bs           blob.Store
	prefix       string
	ttl          time.Duration
	snapshotPath string

	// Change feed: a best-effort accelerator for cross-container
	// read-your-writes. The List-diff refresh above stays the correctness
	// backstop, so any feed error is logged and ignored, never fatal.
	feedPath      string
	feedTTL       time.Duration
	foldThreshold int

	// allocMu serializes barcode allocation. It is deliberately not mu: an
	// allocator holds it across a grain read, a barcode choice and a grain
	// write, and mu is taken and released several times inside that span.
	allocMu sync.Mutex

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
	// barcodeHolders maps a barcode to every item holding it, one entry per item
	// occurrence, each tagged with its work, instance, and whether the work is
	// live (not suppressed/tombstoned). The duplicate report and the
	// write-time uniqueness constraint both derive from it.
	barcodeHolders map[string][]BarcodeHolder
	summaries      []ingest.WorkSummary
	paths          map[string]string
	// generation counts derived-view rebuilds. A consumer that caches something
	// expensive keyed on the corpus (the similarity index) reads it
	// together with the summaries and rebuilds only when it moves.
	generation uint64

	// Snapshot prime drift: after a snapshot load, the first
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
// commit's re-match). Still an ETag diff: only changed grains are
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
// . Both return values are shared: read-only.
func (ix *Index) SummariesWithPaths(ctx context.Context) ([]ingest.WorkSummary, map[string]string, error) {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	if err := ix.freshenLocked(ctx); err != nil {
		return nil, nil, err
	}
	return ix.summaries, ix.paths, nil
}

// SummariesWithGeneration returns every work's summary and the generation of the
// derived view they came from. The pair is read under one lock: a caller that read
// the summaries and the generation separately could cache a stale index under a
// fresh generation and never rebuild it. The slice is shared: read-only.
func (ix *Index) SummariesWithGeneration(ctx context.Context) ([]ingest.WorkSummary, uint64, error) {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	if err := ix.freshenLocked(ctx); err != nil {
		return nil, 0, err
	}
	return ix.summaries, ix.generation, nil
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
			// A tombstoned or suppressed work is already retired -- it is not an
			// actionable merge candidate, and listing it clutters the dedup queue
			// with dead entries. Skip it, mirroring DuplicateBarcodes' live-only
			// filter so the two maintenance reports agree. byCluster
			// itself keeps every work for identity resolution (ClusterOwners); the
			// liveness filter is a report-time concern.
			if e := ix.grains[ref.Path]; e != nil && e.hidden {
				continue
			}
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
// needs from LoadPriorStore, without the per-request corpus read.
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

// MergedInto reports whether workID already records an outgoing merge -- i.e. it
// is a retired loser -- and, if so, the survivor it was merged into. The merge
// marker lives in the survivor's grain (bibframe.AddMergeMarker), not the loser's,
// so a merge endpoint cannot see it by reading the loser's own grain; it consults
// the indexed markers instead. Lets a second, contradictory merge of the same
// loser be refused before it writes a second marker.
func (ix *Index) MergedInto(ctx context.Context, workID string) (string, bool, error) {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	if err := ix.refreshLocked(ctx); err != nil {
		return "", false, err
	}
	for _, e := range ix.grains {
		for _, m := range e.merges {
			if m.From == workID {
				return m.To, true, nil
			}
		}
	}
	return "", false, nil
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
// AllocateBarcodes runs fn holding the process-wide barcode allocation lock.
//
// Choosing a barcode is a read-modify-write over the whole corpus: read the
// taken set, pick the unused ones, write them into a grain. Barcodes returns a
// copy and reserves nothing, so two allocators that overlap anywhere in that
// span choose the same numbers. The grain write cannot arbitrate it either --
// two bulk adds against two different works compare-and-swap on two different
// objects, and both succeed.
//
// A barcode names one physical copy, so a duplicate is not a stale read to be
// retried; it is the wrong label on a book. Serializing the whole span is the
// only thing that makes the check the caller performs mean anything.
//
// This is correct for one process, which is the only deployment libcat supports
// today -- signingKey() warns at boot when a second replica would break token
// verification. It is the seam to replace when that changes: the allocation
// needs a conditional write in the shared store, per barcode, rather than a
// mutex in one process's memory.
//
// Safe on a nil index: fn runs unserialized, as it did before this existed.
func (ix *Index) AllocateBarcodes(fn func() error) error {
	if ix == nil {
		return fn()
	}
	ix.allocMu.Lock()
	defer ix.allocMu.Unlock()
	return fn()
}

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

// BarcodeHolder is one item that holds a barcode: its work, its instance, and
// whether that work is live (not suppressed/tombstoned). Uniqueness is judged
// among live holders only -- a withdrawn copy's barcode may be reused.
type BarcodeHolder struct {
	WorkID     string
	InstanceID string
	Live       bool
}

// DuplicateBarcode is one barcode held by more than one live item in the corpus,
// with the works that hold it. A barcode names one physical copy, so a duplicate
// is a data-quality defect a librarian needs to find before uniqueness can be
// enforced on writes.
type DuplicateBarcode struct {
	Barcode string   `json:"barcode"`
	Count   int      `json:"count"`   // number of live items holding it
	WorkIDs []string `json:"workIds"` // the works that hold it, sorted and unique
}

// DuplicateBarcodes reports every barcode held by more than one live item across
// the corpus, sorted by barcode. Count is the number of live item occurrences (so
// a barcode on two items of one work counts twice and is reported); WorkIDs is the
// sorted, de-duplicated set of works holding it.
func (ix *Index) DuplicateBarcodes(ctx context.Context) ([]DuplicateBarcode, error) {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	if err := ix.freshenLocked(ctx); err != nil {
		return nil, err
	}
	var out []DuplicateBarcode
	for bc, holders := range ix.barcodeHolders {
		count := 0
		seen := map[string]bool{}
		var works []string
		for _, h := range holders {
			if !h.Live {
				continue
			}
			count++
			if !seen[h.WorkID] {
				seen[h.WorkID] = true
				works = append(works, h.WorkID)
			}
		}
		if count < 2 {
			continue
		}
		sort.Strings(works)
		out = append(out, DuplicateBarcode{Barcode: bc, Count: count, WorkIDs: works})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Barcode < out[j].Barcode })
	return out, nil
}

// BarcodeHeldByOther reports whether barcode is already held by a live item on a
// different instance than (workID, instanceID) -- the write-time uniqueness check
// . It excludes the given instance so re-saving that instance's own
// items is not a self-collision. Answers from the in-memory holders map under the
// index lock, with no whole-corpus copy ( sizing).
func (ix *Index) BarcodeHeldByOther(ctx context.Context, barcode, workID, instanceID string) (bool, BarcodeHolder, error) {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	if err := ix.freshenLocked(ctx); err != nil {
		return false, BarcodeHolder{}, err
	}
	for _, h := range ix.barcodeHolders[barcode] {
		if h.Live && !(h.WorkID == workID && h.InstanceID == instanceID) {
			return true, h, nil
		}
	}
	return false, BarcodeHolder{}, nil
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
	barcodeHolders := map[string][]BarcodeHolder{}
	summaries := make([]ingest.WorkSummary, 0, len(paths))
	workPaths := make(map[string]string, len(paths))
	for _, p := range paths {
		entry := ix.grains[p]
		workID := strings.TrimSuffix(p[strings.LastIndex(p, "/")+1:], ".nq")
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
		for _, it := range entry.items {
			if it.barcode == "" {
				continue
			}
			barcodeHolders[it.barcode] = append(barcodeHolders[it.barcode],
				BarcodeHolder{WorkID: workID, InstanceID: it.instanceID, Live: !entry.hidden})
		}
		for _, s := range entry.summaries {
			workPaths[s.WorkID] = p
		}
		summaries = append(summaries, entry.summaries...)
	}
	sort.Slice(summaries, func(i, j int) bool { return summaries[i].WorkID < summaries[j].WorkID })
	ix.byProvider, ix.byCluster, ix.barcodes, ix.summaries, ix.paths = byProvider, byCluster, barcodes, summaries, workPaths
	ix.barcodeHolders = barcodeHolders
	ix.dirty = false
	ix.generation++
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
	// item IRI -> instance id, from the bf:hasItem edges, so each barcode can be
	// attributed to its instance. Works for both IRI and blank-node
	// item nodes, unlike parsing the item IRI shape.
	itemInst := map[string]string{}
	for _, q := range ds.Quads {
		if q.P.Value == bibframe.PredHasItem && q.S.IsIRI() {
			itemInst[q.O.Value] = bibframe.FragInstance(q.S.Value)
		}
	}
	ed := bibframe.EditorialGraph()
	for _, q := range ds.Quads {
		if q.P.Value == bibframe.PredBarcode && q.O.IsLiteral() {
			entry.barcodes = append(entry.barcodes, q.O.Value)
			entry.items = append(entry.items, itemBarcode{barcode: q.O.Value, instanceID: itemInst[q.S.Value]})
		}
		// The grain's Work being suppressed or tombstoned makes its items
		// non-live for uniqueness: a withdrawn copy's barcode may be reused.
		if q.G == ed {
			if q.P.Value == bibframe.PredTombstoned || (q.P.Value == bibframe.PredSuppressed && q.O.Value == "true") {
				entry.hidden = true
			}
		}
	}
	return entry, nil
}
