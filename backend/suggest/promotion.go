package suggest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/freeeve/libcat/backend/store"
	"github.com/freeeve/libcat/backend/vocab"
)

// Promotion proposes elevating an uncontrolled tag to a controlled term
// (tasks/044): on approval the publisher rewrites every carrying Work
// (subject added; editorial tag retracted) and records lcat:tagAlias so the
// projector suppresses the tag where the term is present and pickers
// auto-suggest the term. One open promotion per normalized tag.
type Promotion struct {
	Tag        string        `json:"tag"` // normalized (vocab.NormalizeFolk)
	Term       vocab.TermRef `json:"term"`
	Status     Status        `json:"status"` // PENDING -> APPROVED / REJECTED
	ProposedBy string        `json:"proposedBy"`
	CreatedAt  time.Time     `json:"createdAt"`
	DecidedBy  string        `json:"decidedBy,omitempty"`
	DecidedAt  time.Time     `json:"decidedAt,omitzero"`
	// Works counts the rewrites at execution (stamped by the publisher).
	Works int `json:"works,omitempty"`
}

// ErrPromotionExists reports an already-open proposal for the tag.
var ErrPromotionExists = errors.New("suggest: promotion already proposed for this tag")

func promotionKey(tag string) store.Key {
	return store.Key{PK: "PROMOS", SK: tag}
}

// ProposePromotion records a pending promotion. The target term must
// resolve in the vocabulary index -- promotions are into loaded controlled
// vocabularies.
func (s *Service) ProposePromotion(ctx context.Context, rawTag string, term vocab.TermRef, actor string) (Promotion, error) {
	tag, err := vocab.NormalizeFolk(rawTag)
	if err != nil {
		return Promotion{}, ErrBadTerm
	}
	if s.vocab == nil {
		return Promotion{}, ErrBadTerm
	}
	resolved, ok := s.vocab.Lookup(term.Scheme, term.ID)
	if !ok {
		return Promotion{}, ErrBadTerm
	}
	term.Label = resolved.Label("")
	p := Promotion{
		Tag: tag, Term: term, Status: StatusPending,
		ProposedBy: actor, CreatedAt: s.now().UTC(),
	}
	data, err := json.Marshal(p)
	if err != nil {
		return Promotion{}, err
	}
	if _, err := s.db.Put(ctx, store.Record{Key: promotionKey(tag), Data: data}, store.CondIfAbsent); err != nil {
		if errors.Is(err, store.ErrConditionFailed) {
			// A rejected prior proposal may be superseded; a pending or
			// approved one may not.
			existing, gerr := s.GetPromotion(ctx, tag)
			if gerr == nil && existing.Status == StatusRejected {
				rec, _ := s.db.Get(ctx, promotionKey(tag))
				rec.Data = data
				if _, perr := s.db.Put(ctx, rec, store.CondIfVersion); perr == nil {
					s.writeAudit(ctx, AuditEntry{Action: "PROMOTION_PROPOSE", Actor: actor,
						Terms: []string{vocab.FolkScheme + ":" + tag, term.Scheme + ":" + term.ID}})
					return p, nil
				}
			}
			return Promotion{}, ErrPromotionExists
		}
		return Promotion{}, err
	}
	s.writeAudit(ctx, AuditEntry{Action: "PROMOTION_PROPOSE", Actor: actor,
		Terms: []string{vocab.FolkScheme + ":" + tag, term.Scheme + ":" + term.ID}})
	return p, nil
}

// GetPromotion returns one promotion by normalized tag.
func (s *Service) GetPromotion(ctx context.Context, tag string) (Promotion, error) {
	rec, err := s.db.Get(ctx, promotionKey(tag))
	if err != nil {
		return Promotion{}, err
	}
	var p Promotion
	if err := json.Unmarshal(rec.Data, &p); err != nil {
		return Promotion{}, err
	}
	return p, nil
}

// Promotions lists proposals, optionally by status.
func (s *Service) Promotions(ctx context.Context, status Status) ([]Promotion, error) {
	out := []Promotion{}
	for rec, err := range s.db.Query(ctx, "PROMOS", "", store.QueryOpt{}) {
		if err != nil {
			return nil, err
		}
		var p Promotion
		if json.Unmarshal(rec.Data, &p) != nil {
			continue
		}
		if status != "" && p.Status != status {
			continue
		}
		out = append(out, p)
	}
	return out, nil
}

// DecidePromotion resolves a pending proposal. Rejection just stamps it;
// approval stamps it and returns the promotion for the publisher to execute
// (the caller runs Publisher.PromoteTag and then MarkPromotionExecuted).
func (s *Service) DecidePromotion(ctx context.Context, tag string, approve bool, actor string) (Promotion, error) {
	var decided Promotion
	err := s.mutatePromotion(ctx, tag, func(p *Promotion) error {
		if p.Status != StatusPending {
			return fmt.Errorf("suggest: promotion for %q already %s", tag, p.Status)
		}
		p.Status = StatusRejected
		if approve {
			p.Status = StatusApproved
		}
		p.DecidedBy = actor
		p.DecidedAt = s.now().UTC()
		decided = *p
		return nil
	})
	if err != nil {
		return Promotion{}, err
	}
	action := "PROMOTION_REJECT"
	if approve {
		action = "PROMOTION_APPROVE"
	}
	s.writeAudit(ctx, AuditEntry{Action: action, Actor: actor,
		Terms: []string{vocab.FolkScheme + ":" + tag, decided.Term.Scheme + ":" + decided.Term.ID}})
	return decided, nil
}

// MarkPromotionExecuted stamps the rewrite count after the publisher runs.
func (s *Service) MarkPromotionExecuted(ctx context.Context, tag string, works int) error {
	return s.mutatePromotion(ctx, tag, func(p *Promotion) error {
		p.Works = works
		return nil
	})
}

func (s *Service) mutatePromotion(ctx context.Context, tag string, mutate func(*Promotion) error) error {
	return s.casUpdate(ctx, promotionKey(tag), "suggest: promotion update conflict", false, func(data []byte, _ bool) ([]byte, error) {
		var p Promotion
		if err := json.Unmarshal(data, &p); err != nil {
			return nil, err
		}
		if err := mutate(&p); err != nil {
			return nil, err
		}
		return json.Marshal(p)
	})
}
