package suggest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/freeeve/libcat/backend/store"
	"github.com/freeeve/libcat/backend/vocab"
)

// Decision is one staff review action inside a batch.
type Decision struct {
	WorkID  string        `json:"workId"`
	Term    vocab.TermRef `json:"term"`
	Type    SuggType      `json:"type"`
	Approve bool          `json:"approve"`
	// SubstituteTerm, when set on an approval, records that the reviewer
	// slid to a neighbouring vocabulary term instead of the suggested one.
	SubstituteTerm *vocab.TermRef `json:"substituteTerm,omitempty"`
	Note           string         `json:"note,omitempty"`
	// Tombstone on a rejection also blocks future re-suggestions of the pair.
	Tombstone bool `json:"tombstone,omitempty"`
}

// AuditEntry records a staff decision or publish event. Editorial history for
// the published state itself lives in the grain store; this trail covers
// queue-side actions.
type AuditEntry struct {
	WorkID string    `json:"workId,omitempty"`
	At     time.Time `json:"at"`
	Action string    `json:"action"` // REVIEW_APPROVE, REVIEW_REJECT, MANUAL_TERM, FOLK_ACCEPT, FOLK_BLOCK, PUBLISH_*
	Actor  string    `json:"actor"`
	Terms  []string  `json:"terms,omitempty"` // "<scheme>:<id>"
	Note   string    `json:"note,omitempty"`
	ETag   string    `json:"etag,omitempty"` // grain etag for publish events
	// RunID ties a bulk run's per-record entries to its aggregate entry
	//. Empty for single-record actions, which are their own run.
	RunID string `json:"runId,omitempty"`
}

// auditSKLayout keys an audit entry by time. It is RFC3339 with a **fixed
// width** nanosecond field, unlike time.RFC3339Nano, which trims trailing
// zeros: an entry at .167790000 keyed as ".16779Z" and one at .167792000 as
// ".167792Z", and a descending lexicographic scan ranks 'Z' (0x5A) above '2',
// so the older entry came back first. A bulk run writes several entries inside
// one microsecond, which is what surfaced it.
const auditSKLayout = "2006-01-02T15:04:05.000000000Z07:00"

// NewRunID mints the identifier shared by one bulk run's audit entries.
func NewRunID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// maxNotedWorks bounds the ids a RunNote carries. A run over the whole catalog
// would otherwise put thousands of them in one audit record.
const maxNotedWorks = 100

// RunNote is the note a bulk run's aggregate audit entry carries: the run's
// counts and the records it rewrote, as JSON.
//
// The split is deliberate. A per-record entry's note is prose, because it is
// read by a cataloger in that record's History tab. The aggregate entry appears
// only on the Audit screen and exists to answer "what did that run touch?", so
// it is machine-readable and names its records.
//
// It replaces a note built by marshalling the results and cutting the bytes at
// 512, which past a handful of works stopped being parseable and could split a
// UTF-8 rune at the boundary. This truncates the *list*, and says how many it
// dropped.
type RunNote struct {
	Selection string   `json:"selection,omitempty"`
	Matched   int      `json:"matched"`
	Applied   int      `json:"applied"`
	Rewritten int      `json:"rewritten"`
	Failed    int      `json:"failed"`
	Added     int      `json:"added"`
	Removed   int      `json:"removed"`
	Works     []string `json:"works"`
	More      int      `json:"more,omitempty"`
}

// String renders the note as JSON, capping the work list and recording how many
// ids it left out rather than dropping them silently.
func (n RunNote) String() string {
	if len(n.Works) > maxNotedWorks {
		n.More = len(n.Works) - maxNotedWorks
		n.Works = n.Works[:maxNotedWorks]
	}
	if n.Works == nil {
		n.Works = []string{}
	}
	b, err := json.Marshal(n)
	if err != nil {
		return ""
	}
	return string(b)
}

// QueueQuery filters the review queue.
type QueueQuery struct {
	Status     Status // default PENDING
	Scheme     string
	Provenance Provenance
	Type       SuggType
	Limit      int    // default 50
	Cursor     string // opaque; from a prior QueuePage
}

// QueuePage is one page of the review queue, supporter-count-descending.
type QueuePage struct {
	Items  []Suggestion `json:"items"`
	Cursor string       `json:"cursor,omitempty"`
}

// Queue lists aggregates in the requested status. It reads the status index
// partition and hydrates each aggregate; index items whose aggregate has
// moved on are deleted in passing (the index is repairable, the aggregate is
// truth).
func (s *Service) Queue(ctx context.Context, q QueueQuery) (QueuePage, error) {
	if q.Status == "" {
		q.Status = StatusPending
	}
	if q.Limit <= 0 || q.Limit > 200 {
		q.Limit = 50
	}
	page := QueuePage{Items: []Suggestion{}}
	statusPK := "STATUS#" + string(q.Status)
	for rec, err := range s.db.Query(ctx, statusPK, "", store.QueryOpt{StartAfter: q.Cursor}) {
		if err != nil {
			return QueuePage{}, err
		}
		aggKey, err := aggKeyFromIndexSK(rec.Key.SK)
		if err != nil {
			continue
		}
		agg, err := s.db.Get(ctx, aggKey)
		if errors.Is(err, store.ErrNotFound) {
			_ = s.db.Delete(ctx, rec, store.CondNone)
			continue
		}
		if err != nil {
			return QueuePage{}, err
		}
		sg, err := unmarshalSuggestion(agg.Data)
		if err != nil {
			continue
		}
		if sg.Status != q.Status {
			// Stale index item; self-heal.
			_ = s.db.Delete(ctx, rec, store.CondNone)
			continue
		}
		if (q.Scheme != "" && sg.Term.Scheme != q.Scheme) ||
			(q.Provenance != "" && sg.Provenance != q.Provenance) ||
			(q.Type != "" && sg.Type != q.Type) {
			continue
		}
		page.Items = append(page.Items, sg)
		if len(page.Items) >= q.Limit {
			page.Cursor = rec.Key.SK
			break
		}
	}
	// Highest support first within the page (qllpoc's presentation order).
	sort.SliceStable(page.Items, func(i, j int) bool {
		return page.Items[i].SupporterCount > page.Items[j].SupporterCount
	})
	return page, nil
}

// ReviewResult reports what a Review batch actually did. Skipped carries the
// decisions that lost the race -- another moderator had already resolved the
// suggestion, or it no longer exists -- so a caller can tell the human whose
// decision was discarded rather than counting the request back to them
// .
type ReviewResult struct {
	Applied int        `json:"applied"`
	Skipped []Decision `json:"skipped,omitempty"`
}

// Review applies a batch of staff decisions. Each decision flips a
// PENDING/DISPUTED aggregate to APPROVED or REJECTED, stamps the reviewer,
// and writes an audit entry. Rejections may tombstone the pair. Decisions
// against already-resolved items are skipped, not errors -- two reviewers
// may race on a hot queue -- and the ReviewResult says which.
func (s *Service) Review(ctx context.Context, decisions []Decision, actor string) (ReviewResult, error) {
	var res ReviewResult
	now := s.now().UTC()
	for _, d := range decisions {
		to, action := StatusRejected, "REVIEW_REJECT"
		if d.Approve {
			to, action = StatusApproved, "REVIEW_APPROVE"
		}
		// Concern decisions are resolve/dismiss: same transitions, legible
		// audit actions, and nothing ever publishes (the publisher's
		// worklist filters TypeConcern).
		if d.Type == TypeConcern {
			if d.Approve {
				action = "CONCERN_RESOLVE"
			} else {
				action = "CONCERN_DISMISS"
			}
		}
		if d.Approve && d.SubstituteTerm != nil {
			if s.vocab == nil {
				return res, ErrBadTerm
			}
			if _, ok := s.vocab.Lookup(d.SubstituteTerm.Scheme, d.SubstituteTerm.ID); !ok {
				return res, fmt.Errorf("%w: substitute %s:%s", ErrBadTerm, d.SubstituteTerm.Scheme, d.SubstituteTerm.ID)
			}
		}
		key := store.Key{PK: workPK(d.WorkID), SK: suggSK(d.Term, d.Type)}
		err := s.transition(ctx, key, to, func(sg *Suggestion) {
			sg.ReviewedAt = now
			sg.ReviewedBy = actor
			sg.ReviewNote = d.Note
			if d.Approve && d.SubstituteTerm != nil {
				sub := *d.SubstituteTerm
				sg.SubstituteTerm = &sub
			}
		})
		// Someone else resolved it first, or it is gone. The decision is
		// discarded; say so rather than counting it as reviewed.
		if errors.Is(err, errAlreadyResolved) || errors.Is(err, store.ErrNotFound) {
			res.Skipped = append(res.Skipped, d)
			continue
		}
		if err != nil {
			return res, fmt.Errorf("suggest: review %s/%s: %w", d.WorkID, d.Term.ID, err)
		}
		if !d.Approve && d.Tombstone {
			if err := s.WriteTombstone(ctx, d.WorkID, d.Term, actor); err != nil {
				return res, err
			}
		}
		s.writeAudit(ctx, AuditEntry{
			WorkID: d.WorkID, Action: action, Actor: actor,
			Terms: []string{d.Term.Scheme + ":" + d.Term.ID}, Note: d.Note,
		})
		res.Applied++
	}
	return res, nil
}

// ManualTerm lets a librarian add a term patrons and pipelines missed. The
// aggregate is born APPROVED (the librarian is the review) and flows to the
// graph on the next publish.
func (s *Service) ManualTerm(ctx context.Context, workID string, ref vocab.TermRef, workTitle, actor string) error {
	// patron=false: the librarian is the authority, so the patron-suggestion
	// policy does not gate what they may add.
	term, _, err := s.resolveTerm(ctx, ref, false)
	if err != nil {
		return err
	}
	now := s.now().UTC()
	sg := Suggestion{
		WorkID: workID, Term: term, Type: TypeAdd,
		Status: StatusApproved, Provenance: ProvenanceLibrarian,
		WorkTitle: workTitle, CreatedAt: now, LastActivityAt: now,
		ReviewedAt: now, ReviewedBy: actor,
	}
	data, err := marshalSuggestion(sg)
	if err != nil {
		return err
	}
	key := store.Key{PK: workPK(workID), SK: suggSK(term, TypeAdd)}
	if _, err := s.db.Put(ctx, store.Record{Key: key, Data: data}, store.CondIfAbsent); err != nil {
		if errors.Is(err, store.ErrConditionFailed) {
			return fmt.Errorf("suggest: %s already suggested for %s", term.ID, workID)
		}
		return err
	}
	s.writeStatusIndex(ctx, StatusApproved, key)
	s.writeAudit(ctx, AuditEntry{
		WorkID: workID, Action: "MANUAL_TERM", Actor: actor,
		Terms: []string{term.Scheme + ":" + term.ID},
	})
	return nil
}

// PipelineSuggest lands one machine-produced candidate in the moderation
// queue: a PENDING aggregate with PIPELINE provenance and confidence,
// create-only (an existing aggregate for the pair -- from patrons or a prior
// run -- is left untouched, so re-running an enrichment source never spams
// the queue). The term is deliberately not gated by the vocabulary index:
// enrichment sources assert terms from vocabularies too large to load, and
// moderation is the gate. Tombstoned pairs are skipped silently.
func (s *Service) PipelineSuggest(ctx context.Context, workID string, term vocab.TermRef, confidence float64) error {
	if _, err := s.db.Get(ctx, store.Key{PK: workPK(workID), SK: tombstoneSK(term)}); err == nil {
		return nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return err
	}
	now := s.now().UTC()
	sg := Suggestion{
		WorkID: workID, Term: term, Type: TypeAdd,
		Status: StatusPending, Provenance: ProvenancePipeline,
		Confidence: confidence, CreatedAt: now, LastActivityAt: now,
	}
	data, err := marshalSuggestion(sg)
	if err != nil {
		return err
	}
	key := store.Key{PK: workPK(workID), SK: suggSK(term, TypeAdd)}
	if _, err := s.db.Put(ctx, store.Record{Key: key, Data: data}, store.CondIfAbsent); err != nil {
		if errors.Is(err, store.ErrConditionFailed) {
			return nil // pair already suggested; moderation owns it
		}
		return err
	}
	s.writeStatusIndex(ctx, StatusPending, key)
	return nil
}

// SetFolkStatus accepts or blocks a folksonomy term. Accepted terms enter
// the autocomplete index; blocking removes them from it and refuses future
// suggestions.
func (s *Service) SetFolkStatus(ctx context.Context, norm string, status FolkStatus, actor string) error {
	if status != FolkAccepted && status != FolkBlocked {
		return fmt.Errorf("suggest: invalid folk status %q", status)
	}
	if err := s.mutateFolk(ctx, norm, func(ft *FolkTerm) { ft.Status = status }); err != nil {
		return err
	}
	idxKey := store.Key{PK: "FOLKIDX", SK: "TERM#" + norm}
	if status == FolkAccepted {
		if _, err := s.db.Put(ctx, store.Record{Key: idxKey, Data: []byte(norm)}, store.CondNone); err != nil {
			return err
		}
	} else {
		_ = s.db.Delete(ctx, store.Record{Key: idxKey}, store.CondNone)
	}
	action := "FOLK_ACCEPT"
	if status == FolkBlocked {
		action = "FOLK_BLOCK"
	}
	s.writeAudit(ctx, AuditEntry{Action: action, Actor: actor, Terms: []string{vocab.FolkScheme + ":" + norm}})
	return nil
}

// WriteTombstone blocks future suggestions of a (work, term) pair.
func (s *Service) WriteTombstone(ctx context.Context, workID string, term vocab.TermRef, actor string) error {
	data, err := json.Marshal(map[string]string{"actor": actor})
	if err != nil {
		return err
	}
	rec := store.Record{
		Key:  store.Key{PK: workPK(workID), SK: tombstoneSK(term)},
		Data: data,
	}
	if _, err := s.db.Put(ctx, rec, store.CondNone); err != nil {
		return fmt.Errorf("suggest: write tombstone: %w", err)
	}
	return nil
}

// ApprovedUnpublished lists APPROVED aggregates not yet carried into the
// graph -- the publisher's worklist.
func (s *Service) ApprovedUnpublished(ctx context.Context) ([]Suggestion, error) {
	var out []Suggestion
	for rec, err := range s.db.Query(ctx, "STATUS#"+string(StatusApproved), "", store.QueryOpt{}) {
		if err != nil {
			return nil, err
		}
		aggKey, err := aggKeyFromIndexSK(rec.Key.SK)
		if err != nil {
			continue
		}
		agg, err := s.db.Get(ctx, aggKey)
		if err != nil {
			continue
		}
		sg, err := unmarshalSuggestion(agg.Data)
		if err != nil || sg.Status != StatusApproved || sg.PublishedETag != "" || sg.Type == TypeConcern {
			continue
		}
		out = append(out, sg)
	}
	return out, nil
}

// MarkPublished stamps aggregates with the grain etag that carried them.
func (s *Service) MarkPublished(ctx context.Context, items []Suggestion, etag string) error {
	now := s.now().UTC()
	for _, sg := range items {
		key := store.Key{PK: workPK(sg.WorkID), SK: suggSK(sg.Term, sg.Type)}
		err := s.mutateSuggestion(ctx, key, func(cur *Suggestion) {
			cur.PublishedAt = now
			cur.PublishedETag = etag
		})
		if err != nil {
			return fmt.Errorf("suggest: mark published %s/%s: %w", sg.WorkID, sg.Term.ID, err)
		}
	}
	return nil
}

// mutateSuggestion applies mutate to an aggregate under optimistic
// concurrency without status-transition rules.
func (s *Service) mutateSuggestion(ctx context.Context, key store.Key, mutate func(*Suggestion)) error {
	return s.casUpdate(ctx, key, "suggest: update conflict", false, func(data []byte, _ bool) ([]byte, error) {
		sg, err := unmarshalSuggestion(data)
		if err != nil {
			return nil, err
		}
		mutate(&sg)
		return marshalSuggestion(sg)
	})
}

// WriteAudit records a publish-lifecycle event not tied to one decision.
func (s *Service) WriteAudit(ctx context.Context, entry AuditEntry) {
	s.writeAudit(ctx, entry)
}

func (s *Service) writeAudit(ctx context.Context, entry AuditEntry) {
	now := s.now().UTC()
	entry.At = now
	suffix := make([]byte, 4)
	_, _ = rand.Read(suffix)
	data, err := json.Marshal(entry)
	if err != nil {
		return
	}
	key := store.Key{
		PK: "AUDIT#" + now.Format("2006-01"),
		SK: now.Format(auditSKLayout) + "#" + hex.EncodeToString(suffix),
	}
	_, _ = s.db.Put(ctx, store.Record{Key: key, Data: data}, store.CondIfAbsent)
}

// Audit returns a month's audit trail, newest first.
//
// The sort is not redundant with the descending key scan. Entries written
// before auditSKLayout keyed on time.RFC3339Nano, which trims trailing zeros
// from the fractional second: ".16779Z" (167790ns) sorts above ".167792Z" in
// lexicographic order, because 'Z' outranks '2'. Those keys are still in the
// store, and sorting on the timestamp itself puts them back in order.
func (s *Service) Audit(ctx context.Context, month string) ([]AuditEntry, error) {
	var out []AuditEntry
	for rec, err := range s.db.Query(ctx, "AUDIT#"+month, "", store.QueryOpt{Descending: true}) {
		if err != nil {
			return nil, err
		}
		var e AuditEntry
		if err := json.Unmarshal(rec.Data, &e); err != nil {
			continue
		}
		out = append(out, e)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].At.After(out[j].At) })
	return out, nil
}
