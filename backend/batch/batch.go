// Package batch is the one op-list machinery behind Koha's batch record
// modification, MARC modification templates, and advanced-editor macros
// : a Selection names a set of Works, an editor.Op list names the
// edit, and the executor applies it per grain with per-record results --
// dry-run first, exact quad deltas, everything audited. Saved queries and
// macros live in the datastore; a shared macro run over a selection is the
// modification-template shape.
package batch

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/freeeve/libcat/bibframe"
	"github.com/freeeve/libcat/ingest"
	"github.com/freeeve/libcat/storage/blob"

	"github.com/freeeve/libcat/backend/editor"
	"github.com/freeeve/libcat/backend/publish"
	"github.com/freeeve/libcat/backend/store"
	"github.com/freeeve/libcat/backend/suggest"
	"github.com/freeeve/libcat/backend/trigger"
)

// Selection kinds. importBatch is reserved for copy cataloging
// and rejected until an import surface exists.
const (
	KindIDs         = "ids"
	KindSearch      = "search"
	KindSavedQuery  = "savedQuery"
	KindAll         = "all"
	KindImportBatch = "importBatch"
)

// ErrValidation reports a request the executor refuses to run.
var ErrValidation = errors.New("batch: invalid request")

// Bounds: ops per run mirror the single-record cap; work count is bounded so
// a stray "all" on a large corpus is an explicit decision, not an accident.
const (
	maxOps          = 200
	defaultMaxWorks = 2000
	// maxItemDiffs caps per-work diffs carried in a dry-run result; the
	// aggregate counts always cover the full selection and the result says
	// when diffs were truncated (no silent caps).
	maxItemDiffs = 50
)

var workIDPattern = regexp.MustCompile(`^w[a-z0-9]{6,20}$`)

// Selection names a set of Works to operate on.
type Selection struct {
	Kind         string   `json:"kind"`
	IDs          []string `json:"ids,omitempty"`          // kind=ids
	Query        string   `json:"query,omitempty"`        // kind=search
	SavedQueryID string   `json:"savedQueryId,omitempty"` // kind=savedQuery
	// Facets narrows kind=search and kind=all by the same dimensions the works
	// rail offers -- AND across groups, OR within one. Without them
	// "Export these results…" resolved to the whole catalog while the screen
	// beside it said 465.
	//
	// They do not rescue kind=search from an empty query: KindAll remains the
	// only way to say "everything", so a whitespace-only search cannot select
	// the catalog by accident. "Everything, filtered" is
	// kind=all + facets.
	Facets map[string][]string `json:"facets,omitempty"`
	// Tombstoned is exclude|include|only over retired records. Unlike the works
	// listing, which defaults to exclude, a selection defaults to **include**:
	// "Entire catalog" has always meant the entire catalog, and a silent change
	// there would quietly drop records from everyone's exports. The works screen
	// sends "exclude" explicitly so its count and the export's agree.
	Tombstoned string `json:"tombstoned,omitempty"`
}

// keep returns the predicate a selection's Facets and Tombstoned impose.
func (sel Selection) keep() (func(ingest.WorkSummary) bool, error) {
	var tomb func(ingest.WorkSummary) bool
	switch sel.Tombstoned {
	case "", "include":
		tomb = func(ingest.WorkSummary) bool { return true }
	case "exclude":
		tomb = func(s ingest.WorkSummary) bool { return !s.Tombstoned }
	case "only":
		tomb = func(s ingest.WorkSummary) bool { return s.Tombstoned }
	default:
		return nil, fmt.Errorf("%w: tombstoned must be exclude|include|only", ErrValidation)
	}
	return func(s ingest.WorkSummary) bool {
		return tomb(s) && ingest.MatchesFacets(s, sel.Facets)
	}, nil
}

// Target is one resolved Work.
type Target struct {
	WorkID string `json:"workId"`
	Title  string `json:"title,omitempty"`
	path   string
}

// ItemResult is one Work's outcome in a run.
type ItemResult struct {
	WorkID string       `json:"workId"`
	ETag   string       `json:"etag,omitempty"`
	Error  string       `json:"error,omitempty"`
	Diff   *editor.Diff `json:"diff,omitempty"`
}

// RunResult summarizes a batch run. Added/Removed aggregate the quad deltas
// across every work (dry-run and execute alike).
type RunResult struct {
	DryRun         bool         `json:"dryRun"`
	Matched        int          `json:"matched"`
	Applied        int          `json:"applied"`
	Failed         int          `json:"failed"`
	Added          int          `json:"added"`
	Removed        int          `json:"removed"`
	Results        []ItemResult `json:"results"`
	DiffsTruncated bool         `json:"diffsTruncated,omitempty"`
}

// Service resolves selections and executes op batches over the grain tree.
type Service struct {
	Blob blob.Store
	DB   store.Store
	// Mapper is the static op mapper. MapperFn, when set, supersedes it so
	// runtime profile edits are picked up per run; leave Mapper for the
	// fixed-profile case (tests).
	Mapper   *editor.Mapper
	MapperFn func() *editor.Mapper
	// Queue, when set, receives one audit entry per execute run.
	Queue *suggest.Service
	// Trigger, when set, gets one grains-changed event per execute run.
	Trigger trigger.Notifier
	// Prefix is the grain tree root ("" = repo layout).
	Prefix string
	// MaxWorks bounds a run's selection (0 = defaultMaxWorks).
	MaxWorks int
	// Summaries, when set, is the shared maintained summary source
	// (workindex) search selections resolve against instead of a
	// per-run corpus walk; nil falls back to ScanSummaries.
	Summaries ingest.SummarySource
	// Labels, when set, writes vocabulary label companions next to term
	// IRIs a batch edit asserts (editor.ApplyOps).
	Labels editor.LabelResolver
	// Index, when set, is kept exact for this run's own writes -- the same
	// read-your-writes contract the single-record path holds.
	// Without it, batch edits wait out the workindex refresh TTL: invisible
	// to work search for up to 30s, and a chained batch selection resolves
	// against the stale index.
	Index IndexUpdater
	// Logger, when set, receives the raw store error behind a per-record
	// failure. The client is shown a mapped message instead.
	Logger *slog.Logger
}

// IndexUpdater is the workindex.Index surface batch writes keep exact:
// Apply folds one written grain in, AppendFeed publishes the changed paths
// so other containers read-their-writes without a corpus List.
type IndexUpdater interface {
	Apply(grainPath, etag string, grain []byte)
	AppendFeed(ctx context.Context, paths ...string) error
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// Resolve expands a selection to its targets, owner-scoped for saved
// queries. Ids resolve without titles (no corpus scan); search kinds carry
// the summary title for preview listings.
func (s *Service) Resolve(ctx context.Context, sel Selection, owner string) ([]Target, error) {
	switch sel.Kind {
	case KindIDs:
		if len(sel.IDs) == 0 {
			return nil, fmt.Errorf("%w: ids selection needs work ids", ErrValidation)
		}
		seen := map[string]bool{}
		var targets []Target
		for _, id := range sel.IDs {
			if !workIDPattern.MatchString(id) {
				return nil, fmt.Errorf("%w: bad work id %q", ErrValidation, id)
			}
			if seen[id] {
				continue
			}
			seen[id] = true
			targets = append(targets, Target{WorkID: id, path: s.Prefix + bibframe.GrainPath(id)})
		}
		return targets, nil
	case KindSearch:
		// Validate the NORMALIZED query: the scan trims and lowercases, so
		// a raw check let " " through and an empty normalized query means
		// no filter at all -- a whitespace-only search silently selected
		// the entire catalog. KindAll is the only way to say
		// "everything".
		q := normQuery(sel.Query)
		if q == "" {
			return nil, fmt.Errorf("%w: search selection needs a query", ErrValidation)
		}
		keep, err := sel.keep()
		if err != nil {
			return nil, err
		}
		return s.scan(ctx, q, keep)
	case KindSavedQuery:
		if sel.SavedQueryID == "" {
			return nil, fmt.Errorf("%w: savedQuery selection needs an id", ErrValidation)
		}
		sq, err := s.GetQuery(ctx, owner, sel.SavedQueryID)
		if err != nil {
			return nil, err
		}
		// A legacy saved query that normalizes to nothing fails closed
		// here instead of meaning "entire catalog" forever.
		q := normQuery(sq.Query)
		if q == "" {
			return nil, fmt.Errorf("%w: saved query %q has an empty query", ErrValidation, sq.Label)
		}
		keep, err := sel.keep()
		if err != nil {
			return nil, err
		}
		return s.scan(ctx, q, keep)
	case KindAll:
		keep, err := sel.keep()
		if err != nil {
			return nil, err
		}
		return s.scanAll(ctx, keep)
	case KindImportBatch:
		return nil, fmt.Errorf("%w: importBatch selections arrive with copy cataloging", ErrValidation)
	}
	return nil, fmt.Errorf("%w: unknown selection kind %q", ErrValidation, sel.Kind)
}

// scan resolves the corpus summaries (shared index when wired, fresh walk
// otherwise) and filters by the shared summary matcher, so a batch search
// selects exactly what the works search shows. An empty normalized query is
// refused outright -- only scanAll (KindAll's path) selects unfiltered, so
// "no query" can never silently mean "everything".
func (s *Service) scan(ctx context.Context, query string, keep func(ingest.WorkSummary) bool) ([]Target, error) {
	q := normQuery(query)
	if q == "" {
		return nil, fmt.Errorf("%w: search selection needs a query", ErrValidation)
	}
	return s.walk(ctx, func(summary ingest.WorkSummary) bool {
		return summary.Matches(q) && keep(summary)
	})
}

// scanAll is the deliberate whole-catalog selection behind KindAll. keep still
// applies: "the whole catalog" is what the facets say it is.
func (s *Service) scanAll(ctx context.Context, keep func(ingest.WorkSummary) bool) ([]Target, error) {
	return s.walk(ctx, keep)
}

// walk resolves every summary the predicate accepts into a Target.
func (s *Service) walk(ctx context.Context, keep func(ingest.WorkSummary) bool) ([]Target, error) {
	summaries, paths, err := ingest.SummariesOf(ctx, s.Summaries, s.Blob, s.Prefix+"data/works/")
	if err != nil {
		return nil, err
	}
	targets := make([]Target, 0, len(summaries))
	for _, summary := range summaries {
		if !keep(summary) {
			continue
		}
		targets = append(targets, Target{WorkID: summary.WorkID, Title: summary.Title, path: paths[summary.WorkID]})
	}
	return targets, nil
}

// Run applies ops to every work in the selection. Ops must target the work
// resource -- instance ids are per-record and meaningless across a selection.
// DryRun reads and diffs without writing; execute CAS-writes each grain and
// reports per-record success or failure, then audits and notifies once.
func (s *Service) Run(ctx context.Context, sel Selection, ops []editor.Op, dryRun bool, actor string) (RunResult, error) {
	if len(ops) == 0 || len(ops) > maxOps {
		return RunResult{}, fmt.Errorf("%w: 1-%d ops per run", ErrValidation, maxOps)
	}
	for _, op := range ops {
		// Instance ids are minted per grain, so naming one across a selection
		// edits whichever record happens to own it. Items are addressed as a
		// set (editor.ResourceItems), never by id, which is why they are the
		// one non-work resource a batch may reach.
		if op.Resource != "" && op.Resource != "work" && op.Resource != editor.ResourceItems {
			return RunResult{}, fmt.Errorf("%w: batch ops must target the work or items resource, not instance %q", ErrValidation, op.Resource)
		}
	}
	targets, err := s.Resolve(ctx, sel, actor)
	if err != nil {
		return RunResult{}, err
	}
	maxWorks := s.MaxWorks
	if maxWorks <= 0 {
		maxWorks = defaultMaxWorks
	}
	if len(targets) > maxWorks {
		return RunResult{}, fmt.Errorf("%w: selection matches %d works, cap is %d -- narrow the selection", ErrValidation, len(targets), maxWorks)
	}
	result := RunResult{DryRun: dryRun, Matched: len(targets)}
	var changed []string
	// The records the run actually rewrote, captured here rather than read back
	// off result.Results: maxItemDiffs nils the diff of every result past the
	// 50th, so a later read could not tell a rewritten record from an untouched
	// one, and record 51 would silently lose its audit entry.
	var edited []ItemResult
	for _, t := range targets {
		item := s.runOne(ctx, t, ops, dryRun)
		if item.Error != "" {
			result.Failed++
		} else {
			result.Applied++
			if !dryRun {
				changed = append(changed, t.path)
			}
		}
		if item.Diff != nil {
			result.Added += len(item.Diff.Added)
			result.Removed += len(item.Diff.Removed)
			if !dryRun && item.Error == "" && len(item.Diff.Added)+len(item.Diff.Removed) > 0 {
				edited = append(edited, item)
			}
			if len(result.Results) >= maxItemDiffs {
				item.Diff = nil
				result.DiffsTruncated = true
			}
		}
		result.Results = append(result.Results, item)
	}
	// Audit only executions that applied a change: like the single-record
	// ops path, whose audit rides on a successful grain write. In read-only
	// demo mode every grain write fails while the document store stays
	// writable, so an unconditional audit here would durably record demo
	// clicks despite the "nothing is saved" contract.
	if !dryRun && result.Applied > 0 {
		// Publish every written path to the index feed in one append, so
		// other containers read-their-writes too; best-effort, the refresh
		// backstop covers a miss.
		if s.Index != nil && len(changed) > 0 {
			_ = s.Index.AppendFeed(ctx, changed...)
		}
		if s.Queue != nil {
			// One entry per rewritten record, as every other write path does
			//. The aggregate entry alone named no work, so a bulk
			// op was invisible in the History tab of every record it rewrote --
			// and for the search and savedQuery kinds the matched set is not
			// reconstructible afterwards, because the query's results move with
			// the catalog. RunID ties the per-record rows to the aggregate.
			runID := suggest.NewRunID()
			note := fmt.Sprintf("%s selection, %d op%s", sel.Kind, len(ops), plural(len(ops)))
			// A record whose diff is empty was selected but not rewritten, and
			// claiming an edit in its history would be its own kind of lie. The
			// aggregate entry below still records that the run touched it.
			for _, item := range edited {
				s.Queue.WriteAudit(ctx, suggest.AuditEntry{
					WorkID: item.WorkID, Action: "BATCH_OPS", Actor: actor,
					ETag: item.ETag, Note: note, RunID: runID,
				})
			}
			rewritten := make([]string, 0, len(edited))
			for _, item := range edited {
				rewritten = append(rewritten, item.WorkID)
			}
			s.Queue.WriteAudit(ctx, suggest.AuditEntry{
				Action: "BATCH_OPS", Actor: actor, RunID: runID,
				Note: suggest.RunNote{
					Selection: sel.Kind, Matched: result.Matched, Applied: result.Applied,
					Rewritten: len(edited), Failed: result.Failed,
					Added: result.Added, Removed: result.Removed, Works: rewritten,
				}.String(),
			})
		}
		if s.Trigger != nil && len(changed) > 0 {
			_ = s.Trigger.Notify(ctx, trigger.Event{Kind: "grains-changed", Paths: changed, At: time.Now().UTC()})
		}
	}
	return result, nil
}

// runOne applies the ops to a single work. Dry runs diff without writing;
// mapper resolves the op mapper for a run: the live provider when set (so
// runtime profile edits apply), else the static Mapper.
func (s *Service) mapper() *editor.Mapper {
	if s.MapperFn != nil {
		return s.MapperFn()
	}
	return s.Mapper
}

// executes CAS-write and still carry the applied diff for the report.
func (s *Service) runOne(ctx context.Context, t Target, ops []editor.Op, dryRun bool) ItemResult {
	item := ItemResult{WorkID: t.WorkID}
	if dryRun {
		grain, _, err := s.Blob.Get(ctx, t.path)
		if err != nil {
			item.Error = readError(err)
			return item
		}
		updated, err := editor.ApplyOps(s.mapper(), grain, t.WorkID, ops, s.Labels)
		if err != nil {
			item.Error = err.Error()
			return item
		}
		diff := editor.DiffLines(grain, updated)
		item.Diff = &diff
		return item
	}
	var diff editor.Diff
	var written []byte
	etag, err := publish.MutateGrain(ctx, s.Blob, t.path, func(old []byte) ([]byte, error) {
		if len(old) == 0 {
			return nil, errors.New("no such work")
		}
		updated, err := editor.ApplyOps(s.mapper(), old, t.WorkID, ops, s.Labels)
		if err != nil {
			return nil, err
		}
		diff = editor.DiffLines(old, updated)
		written = updated
		return updated, nil
	})
	if err != nil {
		item.Error = writeError(err)
		if s.Logger != nil && item.Error != err.Error() {
			// The operator needs the path and the syscall; the cataloger needs
			// neither and must not be shown either. Before the raw
			// error was rendered into the results list and logged nowhere, so
			// the one reader who could act on it was the one who never saw it.
			s.Logger.Error("batch grain write failed", "work", t.WorkID, "path", t.path, "err", err)
		}
		return item
	}
	if s.Index != nil {
		s.Index.Apply(t.path, etag, written)
	}
	item.ETag = etag
	item.Diff = &diff
	return item
}

func readError(err error) string {
	if errors.Is(err, blob.ErrNotFound) {
		return "no such work"
	}
	return "grain read failed"
}

// ReadOnlyNotice is what a per-record result says when the deployment does not
// accept writes. It must read exactly like httpapi's 403 body for the same
// condition -- a client that can tell the batch route's refusal from the
// single-record route's has learned something about the server it should not
// need to know. TestReadOnlyNoticeMatchesTheGuard pins the pair.
const ReadOnlyNotice = "read-only demo: changes are not saved"

// writeError maps a publish.MutateGrain failure onto a message fit to put in
// front of a cataloger.
//
// The mutate closure's own errors -- "no such work", an op the mapper rejects --
// are the cataloger's answer and pass through unchanged. Everything the store
// produced is named rather than quoted: an *os.PathError carrying the blob root,
// the shard layout and a temp-file name tells the reader nothing they can act on
// and tells them a good deal about the filesystem. "grain write
// failed" says the same amount, and is what the single-record route has always
// answered for the identical failure.
func writeError(err error) string {
	switch {
	case errors.Is(err, blob.ErrReadOnly):
		return ReadOnlyNotice
	case errors.Is(err, publish.ErrGrainConflict):
		return "the record changed while it was being edited, retry"
	case errors.Is(err, publish.ErrGrainStore):
		return "grain write failed"
	}
	return err.Error()
}

// normQuery matches the works listing's treatment: lowercase, trimmed.
func normQuery(q string) string {
	return strings.ToLower(strings.TrimSpace(q))
}
