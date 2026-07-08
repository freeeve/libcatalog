package publish

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/freeeve/libcodex/rdf"

	"github.com/freeeve/libcat/bibframe"
	"github.com/freeeve/libcat/ingest"

	"github.com/freeeve/libcat/backend/suggest"
	"github.com/freeeve/libcat/backend/trigger"
	"github.com/freeeve/libcat/backend/vocab"
)

// aliasGrainPath accumulates the corpus's lcat:tagAlias statements: one
// grain, authority-class graph, preserved across re-ingest like any non-feed
// graph.
const aliasGrainPath = "data/authorities/al/aliases.nq"

// PromoteTag executes an approved tag promotion (tasks/044): every Work
// carrying the tag gains the controlled subject (with its authority labels
// and hierarchy), its editorial lcat:tag is retracted, and the alias grain
// records lcat:tagAlias so the projector suppresses the residual feed tag
// where the term is present and future entries auto-suggest the term.
// Returns the number of Works rewritten.
func (p *Publisher) PromoteTag(ctx context.Context, promo suggest.Promotion, actor string) (int, error) {
	if p.Lease != nil {
		if _, held, err := p.Lease.Held(ctx); err != nil {
			return 0, err
		} else if held {
			return 0, ErrIngestActive
		}
	}
	summaries, paths, err := ingest.SummariesOf(ctx, p.Summaries, p.Blob, p.Prefix+"data/works/")
	if err != nil {
		return 0, err
	}
	subject := p.authoritySubject(promo.Term)
	rewritten := 0
	var changed []string
	for _, summary := range summaries {
		if !slices.Contains(summary.Tags, promo.Tag) {
			continue
		}
		path := paths[summary.WorkID]
		_, err := MutateGrain(ctx, p.Blob, path, func(old []byte) ([]byte, error) {
			updated, err := bibframe.AppendAuthoritySubject(old, summary.WorkID, subject, promo.Term.Scheme)
			if err != nil {
				return nil, err
			}
			// Retract the editorial-side tag if present; a feed-side tag
			// stays and the projector's alias suppression hides it.
			return bibframe.ApplyEditorialPatch(updated, bibframe.Patch{
				Remove: []rdf.Quad{bibframe.TagQuad(summary.WorkID, promo.Tag)},
			})
		})
		if err != nil {
			return rewritten, fmt.Errorf("promote %q on %s: %w", promo.Tag, summary.WorkID, err)
		}
		rewritten++
		changed = append(changed, path)
	}
	if err := p.recordAlias(ctx, promo); err != nil {
		return rewritten, err
	}
	changed = append(changed, p.Prefix+aliasGrainPath)
	p.Queue.WriteAudit(ctx, suggest.AuditEntry{
		Action: "PROMOTION_DONE", Actor: actor,
		Terms: []string{vocab.FolkScheme + ":" + promo.Tag, promo.Term.Scheme + ":" + promo.Term.ID},
		Note:  fmt.Sprintf("%d works rewritten", rewritten),
	})
	if p.Trigger != nil && len(changed) > 0 {
		_ = p.Trigger.Notify(ctx, trigger.Event{Kind: "grains-changed", Paths: changed, At: time.Now().UTC()})
	}
	return rewritten, nil
}

// recordAlias appends the lcat:tagAlias statement to the alias grain
// (creating it on first use).
func (p *Publisher) recordAlias(ctx context.Context, promo suggest.Promotion) error {
	path := p.Prefix + aliasGrainPath
	_, err := MutateGrain(ctx, p.Blob, path, func(old []byte) ([]byte, error) {
		if len(old) == 0 {
			old = []byte{}
		}
		graph := bibframe.AuthorityGraph("aliases")
		return bibframe.ApplyPatch(old, graph, bibframe.Patch{
			Add: []rdf.Quad{bibframe.TagAliasQuad(promo.Term.ID, promo.Tag)},
		})
	})
	return err
}
