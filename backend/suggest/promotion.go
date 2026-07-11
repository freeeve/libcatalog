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
// : on approval the publisher rewrites every carrying Work
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
	// Works counts the grain rewrites the promotion performed, summed across
	// attempts: a rewrite that fails partway records what it managed before the
	// failure, and the retry that finishes it adds its own.
	//
	// It is rewrites, not distinct works, and for a folk tag those are the same
	// number. They diverge only for a tag a provider feed also asserts: PromoteTag
	// retracts the editorial tag but deliberately leaves the feed one (the
	// projector's alias suppression hides it), so a retry matches that work again
	// and counts it again. Safe -- the rewrite is idempotent -- but the sum can
	// then exceed the number of works touched.
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

// ErrPromotionNotPending reports a decision attempted on a promotion that has
// already left PENDING. It exists so a caller can answer 409 without matching on
// a message.
var ErrPromotionNotPending = errors.New("suggest: promotion is not pending")

// decide is the one-way PENDING -> APPROVED|REJECTED transition, stamped in a
// single CAS write together with the rewrite count.
//
// The count travels with the status deliberately. Approving in one
// write and stamping the count in another left a window in which a promotion read
// as APPROVED with `works: 0` -- indistinguishable from an approval whose rewrite
// never ran, which is the state this whole task is about.
func (s *Service) decide(ctx context.Context, tag string, status Status, actor string, works int) (Promotion, error) {
	var decided Promotion
	err := s.mutatePromotion(ctx, tag, func(p *Promotion) error {
		if p.Status != StatusPending {
			return fmt.Errorf("%w: %q is already %s", ErrPromotionNotPending, tag, p.Status)
		}
		p.Status = status
		p.DecidedBy = actor
		p.DecidedAt = s.now().UTC()
		p.Works += works
		decided = *p
		return nil
	})
	if err != nil {
		return Promotion{}, err
	}
	action := "PROMOTION_REJECT"
	if status == StatusApproved {
		action = "PROMOTION_APPROVE"
	}
	s.writeAudit(ctx, AuditEntry{Action: action, Actor: actor,
		Terms: []string{vocab.FolkScheme + ":" + tag, decided.Term.Scheme + ":" + decided.Term.ID}})
	return decided, nil
}

// ApprovePromotion stamps a pending proposal APPROVED, adding works to the count
// already recorded by any failed attempt. Call it only after the rewrite it
// describes has succeeded: the durable record of an intention must not outlive a
// failure to carry it out.
func (s *Service) ApprovePromotion(ctx context.Context, tag, actor string, works int) (Promotion, error) {
	return s.decide(ctx, tag, StatusApproved, actor, works)
}

// RejectPromotion stamps a pending proposal REJECTED. A rejected tag may be
// proposed again; ProposePromotion supersedes it.
func (s *Service) RejectPromotion(ctx context.Context, tag, actor string) (Promotion, error) {
	return s.decide(ctx, tag, StatusRejected, actor, 0)
}

// RecordPromotionWorks adds to the rewrite count without touching the status, so
// a promotion whose rewrite failed partway records how far it got. The promotion
// stays PENDING and the Approve button stays live: PromoteTag skips works that no
// longer carry the tag, so a retry resumes where the failure left off, and the
// counts accumulate across attempts.
func (s *Service) RecordPromotionWorks(ctx context.Context, tag string, works int) error {
	if works == 0 {
		return nil
	}
	return s.mutatePromotion(ctx, tag, func(p *Promotion) error {
		p.Works += works
		return nil
	})
}

// DeletePromotion removes a promotion record outright. It is the escape hatch for
// the states the one-way state machine cannot leave -- notably the promotion a
// deployment with no publisher wired approves but never executes. Deleting frees
// the tag to be proposed again.
func (s *Service) DeletePromotion(ctx context.Context, tag, actor string) error {
	p, err := s.GetPromotion(ctx, tag)
	if err != nil {
		return err
	}
	if err := s.db.Delete(ctx, store.Record{Key: promotionKey(tag)}, store.CondNone); err != nil {
		return err
	}
	s.writeAudit(ctx, AuditEntry{Action: "PROMOTION_DELETE", Actor: actor,
		Terms: []string{vocab.FolkScheme + ":" + tag, p.Term.Scheme + ":" + p.Term.ID}})
	return nil
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
