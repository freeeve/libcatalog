// Package publish carries approved queue decisions into the BIBFRAME grain
// store: editorial quads written under ETag optimistic concurrency, the
// advisory ingest lease, and the downstream rebuild trigger. The write
// discipline (read -> patch -> conditional put -> retry-from-fresh) is safe
// against concurrent editors and re-ingest because editorial statements are
// independent IRI-based quads and canonicalization dedups.
package publish

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/freeeve/libcodex/rdf"

	"github.com/freeeve/libcat/bibframe"
	"github.com/freeeve/libcat/ingest"
	"github.com/freeeve/libcat/storage/blob"

	"github.com/freeeve/libcat/backend/suggest"
	"github.com/freeeve/libcat/backend/trigger"
	"github.com/freeeve/libcat/backend/vocab"
)

// ErrIngestActive reports that the ingest lease is held: approvals stay
// queued (durable) and publishing retries after the lease expires.
var ErrIngestActive = errors.New("publish: ingest lease held; publish deferred")

// casAttempts bounds the conditional-write retry loop per grain.
const casAttempts = 8

// Publisher drains approved-unpublished queue items into grains.
type Publisher struct {
	Blob  blob.Store
	Queue *suggest.Service
	Vocab *vocab.Index
	// Trigger receives one grains-changed event per successful run
	// (nil = no notification).
	Trigger trigger.Notifier
	// Lease, when set, defers publishing while ingest holds it.
	Lease *Lease
	// Prefix is the grain tree root in the blob store ("" = repo-layout
	// paths straight from bibframe.GrainPath).
	Prefix string
	// Summaries, when set, is the shared maintained summary source
	// (workindex, tasks/109) tag promotion scans instead of a per-run
	// corpus walk; nil falls back to ScanSummaries.
	Summaries ingest.SummarySource
	// Index, when set, is kept exact for this publisher's own grain writes
	// -- the read-your-writes contract the single-record and batch paths
	// hold (tasks/195, tasks/203). Without it, tag promotions and approved
	// publishes wait out the workindex refresh TTL: invisible to work
	// search for up to 30s, and batch selections resolve against the
	// stale index meanwhile.
	Index  IndexUpdater
	Logger *slog.Logger
}

// IndexUpdater is the workindex.Index surface publish writes keep exact:
// Apply folds one written grain in, AppendFeed publishes the changed paths
// so other containers read-their-writes (mirrors batch.IndexUpdater).
type IndexUpdater interface {
	Apply(grainPath, etag string, grain []byte)
	AppendFeed(ctx context.Context, paths ...string) error
}

// Result summarizes one publish run.
type Result struct {
	Published int      `json:"published"`
	Skipped   int      `json:"skipped"`
	Paths     []string `json:"paths,omitempty"`
}

// MutateGrain applies mutate to the grain at path under ETag optimistic
// concurrency: read, transform, conditional put, retry from fresh on
// conflict. The exported CAS primitive record editing (tasks/037) and
// store-backed ingest share.
func MutateGrain(ctx context.Context, st blob.Store, path string, mutate func(old []byte) ([]byte, error)) (etag string, err error) {
	for attempt := range casAttempts {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt*attempt) * 5 * time.Millisecond)
		}
		old, oldTag, err := st.Get(ctx, path)
		if err != nil && !errors.Is(err, blob.ErrNotFound) {
			return "", err
		}
		updated, err := mutate(old)
		if err != nil {
			return "", err
		}
		opts := blob.PutOptions{ContentType: "application/n-quads"}
		if oldTag == "" {
			opts.IfNoneMatch = true
		} else {
			opts.IfMatch = oldTag
		}
		newTag, err := st.Put(ctx, path, updated, opts)
		if errors.Is(err, blob.ErrPreconditionFailed) {
			continue // a concurrent writer landed; re-read and re-apply
		}
		if err != nil {
			return "", err
		}
		return newTag, nil
	}
	return "", fmt.Errorf("publish: %s: conditional write kept failing", path)
}

// PublishApproved drains the approved-unpublished worklist: per Work, all
// its approved terms land in one grain mutation; each mutation stamps the
// queue items with the resulting grain ETag and writes an audit entry.
func (p *Publisher) PublishApproved(ctx context.Context, actor string) (Result, error) {
	if p.Lease != nil {
		if holder, held, err := p.Lease.Held(ctx); err != nil {
			return Result{}, err
		} else if held {
			p.logf("publish deferred", "leaseHolder", holder)
			return Result{}, ErrIngestActive
		}
	}
	items, err := p.Queue.ApprovedUnpublished(ctx)
	if err != nil {
		return Result{}, err
	}
	byWork := map[string][]suggest.Suggestion{}
	for _, sg := range items {
		byWork[sg.WorkID] = append(byWork[sg.WorkID], sg)
	}
	result := Result{}
	for workID, group := range byWork {
		path := p.Prefix + bibframe.GrainPath(workID)
		var updated []byte
		etag, err := MutateGrain(ctx, p.Blob, path, func(old []byte) ([]byte, error) {
			if len(old) == 0 {
				return nil, fmt.Errorf("no grain for work %s", workID)
			}
			out, err := p.applyGroup(old, workID, group)
			if err != nil {
				return nil, err
			}
			updated = out
			return out, nil
		})
		if err != nil {
			// A missing grain (retired work, stale queue item) skips;
			// the queue item stays for operator attention.
			p.logf("publish skip", "work", workID, "err", err)
			result.Skipped += len(group)
			continue
		}
		if p.Index != nil {
			p.Index.Apply(path, etag, updated)
		}
		if err := p.Queue.MarkPublished(ctx, group, etag); err != nil {
			return result, err
		}
		terms := make([]string, 0, len(group))
		for _, sg := range group {
			terms = append(terms, sg.Term.Scheme+":"+sg.Term.ID)
		}
		p.Queue.WriteAudit(ctx, suggest.AuditEntry{
			WorkID: workID, Action: "PUBLISH_DONE", Actor: actor, Terms: terms, ETag: etag,
		})
		result.Published += len(group)
		result.Paths = append(result.Paths, path)
	}
	// Publish the writes to the index feed so other containers
	// read-their-writes; best-effort, the refresh backstop covers a miss.
	if p.Index != nil && len(result.Paths) > 0 {
		_ = p.Index.AppendFeed(ctx, result.Paths...)
	}
	if p.Trigger != nil && len(result.Paths) > 0 {
		if err := p.Trigger.Notify(ctx, trigger.Event{Kind: "grains-changed", Paths: result.Paths, At: time.Now().UTC()}); err != nil {
			p.logf("trigger failed", "err", err)
		}
	}
	return result, nil
}

// applyGroup applies one Work's approved suggestions to its grain bytes.
func (p *Publisher) applyGroup(grain []byte, workID string, group []suggest.Suggestion) ([]byte, error) {
	var err error
	for _, sg := range group {
		term := sg.Term
		if sg.SubstituteTerm != nil {
			term = *sg.SubstituteTerm
		}
		switch {
		case term.Scheme == vocab.FolkScheme && sg.Type == suggest.TypeAdd:
			grain, err = bibframe.ApplyEditorialPatch(grain, bibframe.Patch{
				Add: []rdf.Quad{bibframe.TagQuad(workID, term.ID)},
			})
		case sg.Type == suggest.TypeAdd:
			grain, err = bibframe.AppendAuthoritySubject(grain, workID, p.authoritySubject(term), term.Scheme)
			if err == nil {
				grain, err = bibframe.AppendAuthorityTerms(grain, term.Scheme, p.ancestorTerms(term))
			}
		default: // TypeRemove
			grain, err = p.applyRemoval(grain, workID, term)
		}
		if err != nil {
			return nil, fmt.Errorf("apply %s:%s: %w", term.Scheme, term.ID, err)
		}
	}
	return grain, nil
}

// authoritySubject resolves the full term description (multilingual labels,
// broader links) from the vocabulary index, falling back to the queue label.
func (p *Publisher) authoritySubject(term vocab.TermRef) bibframe.AuthoritySubject {
	if p.Vocab != nil {
		if t, ok := p.Vocab.Lookup(term.Scheme, term.ID); ok {
			return bibframe.AuthoritySubject{URI: t.ID, Labels: t.Labels, Broader: t.Broader}
		}
	}
	labels := map[string]string{}
	if term.Label != "" {
		labels[""] = term.Label
	}
	return bibframe.AuthoritySubject{URI: term.ID, Labels: labels}
}

// ancestorTerms resolves the term's transitive skos:broader chain from the
// vocabulary index (tasks/178): the descriptions land in the authority graph
// alongside the subject's own, so the projection can label hierarchy nodes
// no Work carries directly. Nil without an index or for a root term.
func (p *Publisher) ancestorTerms(term vocab.TermRef) []bibframe.AuthoritySubject {
	if p.Vocab == nil {
		return nil
	}
	ancestors := p.Vocab.Ancestors(term.Scheme, term.ID)
	out := make([]bibframe.AuthoritySubject, 0, len(ancestors))
	for _, a := range ancestors {
		out = append(out, bibframe.AuthoritySubject{URI: a.ID, Labels: a.Labels, Broader: a.Broader})
	}
	return out
}

// applyRemoval retracts an editorial-added subject or tag. Removing a
// feed-sourced value needs the lcat:overrides shadowing semantics
// (tasks/042); until then a feed-only value is left in place (the approval
// stays recorded in the queue and audit).
func (p *Publisher) applyRemoval(grain []byte, workID string, term vocab.TermRef) ([]byte, error) {
	if term.Scheme == vocab.FolkScheme {
		return bibframe.ApplyEditorialPatch(grain, bibframe.Patch{
			Remove: []rdf.Quad{bibframe.TagQuad(workID, term.ID)},
		})
	}
	return bibframe.ApplyEditorialPatch(grain, bibframe.Patch{
		Remove: []rdf.Quad{bibframe.SubjectQuad(workID, term.ID)},
	})
}

func (p *Publisher) logf(msg string, args ...any) {
	if p.Logger != nil {
		p.Logger.Info(msg, args...)
	}
}
