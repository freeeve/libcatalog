// Package batch is the one op-list machinery behind Koha's batch record
// modification, MARC modification templates, and advanced-editor macros
// (tasks/047): a Selection names a set of Works, an editor.Op list names the
// edit, and the executor applies it per grain with per-record results --
// dry-run first, exact quad deltas, everything audited. Saved queries and
// macros live in the datastore; a shared macro run over a selection is the
// modification-template shape.
package batch

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/freeeve/libcatalog/bibframe"
	"github.com/freeeve/libcatalog/ingest"
	"github.com/freeeve/libcatalog/storage/blob"

	"github.com/freeeve/libcatalog/backend/editor"
	"github.com/freeeve/libcatalog/backend/publish"
	"github.com/freeeve/libcatalog/backend/store"
	"github.com/freeeve/libcatalog/backend/suggest"
	"github.com/freeeve/libcatalog/backend/trigger"
)

// Selection kinds. importBatch is reserved for copy cataloging (tasks/050)
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
		if sel.Query == "" {
			return nil, fmt.Errorf("%w: search selection needs a query", ErrValidation)
		}
		return s.scan(ctx, sel.Query)
	case KindSavedQuery:
		if sel.SavedQueryID == "" {
			return nil, fmt.Errorf("%w: savedQuery selection needs an id", ErrValidation)
		}
		sq, err := s.GetQuery(ctx, owner, sel.SavedQueryID)
		if err != nil {
			return nil, err
		}
		return s.scan(ctx, sq.Query)
	case KindAll:
		return s.scan(ctx, "")
	case KindImportBatch:
		return nil, fmt.Errorf("%w: importBatch selections arrive with copy cataloging (tasks/050)", ErrValidation)
	}
	return nil, fmt.Errorf("%w: unknown selection kind %q", ErrValidation, sel.Kind)
}

// scan lists the corpus and filters by the shared summary matcher, so a
// batch search selects exactly what the works search shows.
func (s *Service) scan(ctx context.Context, query string) ([]Target, error) {
	summaries, paths, err := ingest.ScanSummaries(ctx, s.Blob, s.Prefix+"data/works/")
	if err != nil {
		return nil, err
	}
	q := normQuery(query)
	var targets []Target
	for _, summary := range summaries {
		if q != "" && !summary.Matches(q) {
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
		if op.Resource != "" && op.Resource != "work" {
			return RunResult{}, fmt.Errorf("%w: batch ops must target the work resource, not instance %q", ErrValidation, op.Resource)
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
			if len(result.Results) >= maxItemDiffs {
				item.Diff = nil
				result.DiffsTruncated = true
			}
		}
		result.Results = append(result.Results, item)
	}
	if !dryRun {
		if s.Queue != nil {
			s.Queue.WriteAudit(ctx, suggest.AuditEntry{
				Action: "BATCH_OPS", Actor: actor,
				Note: fmt.Sprintf("%s selection: %d matched, %d applied, %d failed, +%d/-%d quads",
					sel.Kind, result.Matched, result.Applied, result.Failed, result.Added, result.Removed),
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
		updated, err := editor.ApplyOps(s.mapper(), grain, t.WorkID, ops)
		if err != nil {
			item.Error = err.Error()
			return item
		}
		diff := editor.DiffLines(grain, updated)
		item.Diff = &diff
		return item
	}
	var diff editor.Diff
	etag, err := publish.MutateGrain(ctx, s.Blob, t.path, func(old []byte) ([]byte, error) {
		if len(old) == 0 {
			return nil, errors.New("no such work")
		}
		updated, err := editor.ApplyOps(s.mapper(), old, t.WorkID, ops)
		if err != nil {
			return nil, err
		}
		diff = editor.DiffLines(old, updated)
		return updated, nil
	})
	if err != nil {
		item.Error = err.Error()
		return item
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

// normQuery matches the works listing's treatment: lowercase, trimmed.
func normQuery(q string) string {
	return strings.ToLower(strings.TrimSpace(q))
}
