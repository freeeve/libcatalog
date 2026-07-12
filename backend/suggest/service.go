package suggest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/freeeve/libcat/backend/store"
	"github.com/freeeve/libcat/backend/vocab"
)

// Service is the suggestion queue over the document store. Controlled terms
// are validated against the vocabulary index; folksonomy terms pass through
// the normalizer and their own moderation lifecycle.
type Service struct {
	db    store.Store
	vocab *vocab.Index
	caps  Caps
	now   func() time.Time
	// WorkState, when set, gates intake on the work's existence: it reports
	// whether workID is in the catalog and whether it is tombstoned. Without
	// it (tests, minimal wiring) intake accepts any well-formed id -- the
	// pre-gate behavior that let ghost rows into the queue.
	WorkState func(ctx context.Context, workID string) (exists, tombstoned bool, err error)
}

// New wires the service (zero-value caps fall back to DefaultCaps; a nil
// index rejects every controlled term but folk terms still work).
func New(db store.Store, ix *vocab.Index, caps Caps) *Service {
	if caps.PerDay <= 0 {
		caps.PerDay = DefaultCaps.PerDay
	}
	if caps.PerHour <= 0 {
		caps.PerHour = DefaultCaps.PerHour
	}
	if caps.SupporterTTL <= 0 {
		caps.SupporterTTL = DefaultCaps.SupporterTTL
	}
	return &Service{db: db, vocab: ix, caps: caps, now: time.Now}
}

// SetClock overrides the clock (tests).
func (s *Service) SetClock(now func() time.Time) { s.now = now }

// bumpRate applies the shared per-supporter submission caps: bump-then-
// check windowed counters; a rejected attempt stays counted, which only
// makes the cap stricter under abuse. Term suggestions and concerns share
// one budget.
func (s *Service) bumpRate(ctx context.Context, supporterHash string, now time.Time) error {
	day := now.Format("2006-01-02")
	hour := now.Format("2006010215")
	dayCount, err := s.db.Increment(ctx, store.Key{PK: "RATE#" + supporterHash, SK: "DAY#" + day}, 1, now.Add(48*time.Hour))
	if err != nil {
		return err
	}
	hourCount, err := s.db.Increment(ctx, store.Key{PK: "RATE#" + supporterHash, SK: "HOUR#" + hour}, 1, now.Add(3*time.Hour))
	if err != nil {
		return err
	}
	if dayCount > int64(s.caps.PerDay) || hourCount > int64(s.caps.PerHour) {
		return ErrRateLimited
	}
	return nil
}

// Submit records one anonymous suggestion/flag: term validation, tombstone
// and folk-lifecycle gates, per-supporter rate caps, supporter dedup, then
// the aggregate bump and dispute reconciliation. Unlike qllpoc's single
// TransactWriteItems this is a sequence of conditional writes -- a crash
// mid-sequence can lose one vote's count bump, which is acceptable for
// approximate supporter tallies (review is the arbiter); it can never
// double-count (the dedup marker is first) or corrupt review state.
func (s *Service) Submit(ctx context.Context, in SubmitInput) (SubmitResult, error) {
	if in.Type != TypeAdd && in.Type != TypeRemove {
		return SubmitResult{}, fmt.Errorf("suggest: invalid type %q", in.Type)
	}
	if in.Type == TypeRemove && !validReason(in.Reason) {
		return SubmitResult{}, fmt.Errorf("suggest: invalid reason %q", in.Reason)
	}
	result := SubmitResult{}
	// The patron policy gates this intake: resolveTerm with
	// patron=true refuses a disabled deployment, a scheme outside the allowlist,
	// or a folk tag the free-text mode forbids.
	term, folkNew, err := s.resolveTerm(ctx, in.Term, true)
	if err != nil {
		return SubmitResult{}, err
	}
	in.Term = term
	result.FolkProposed = folkNew

	// Tombstone gate.
	if _, err := s.db.Get(ctx, store.Key{PK: workPK(in.WorkID), SK: tombstoneSK(in.Term)}); err == nil {
		return SubmitResult{}, ErrTombstoned
	} else if !errors.Is(err, store.ErrNotFound) {
		return SubmitResult{}, err
	}

	// Work gate: a suggestion needs a live work. An unknown id and a
	// tombstoned work answer exactly like a tombstoned pair, so the
	// anonymous endpoint never becomes an existence oracle -- and the queue
	// never collects rows pointing at works the catalog does not have.
	if s.WorkState != nil {
		exists, dead, err := s.WorkState(ctx, in.WorkID)
		if err != nil {
			return SubmitResult{}, err
		}
		if !exists || dead {
			return SubmitResult{}, ErrTombstoned
		}
	}

	// Rate caps: bump-then-check; a rejected attempt stays counted, which
	// only makes the cap stricter under abuse.
	now := s.now().UTC()
	if err := s.bumpRate(ctx, in.SupporterHash, now); err != nil {
		return SubmitResult{}, err
	}

	// Supporter dedup marker -- create-only; existing = idempotent no-op.
	marker := store.Record{
		Key:      store.Key{PK: workPK(in.WorkID), SK: suppSK(in.Term, in.Type, in.SupporterHash)},
		ExpireAt: now.Add(s.caps.SupporterTTL),
	}
	if _, err := s.db.Put(ctx, marker, store.CondIfAbsent); err != nil {
		if errors.Is(err, store.ErrConditionFailed) {
			result.Duplicate = true
			return result, nil
		}
		return SubmitResult{}, err
	}

	// Aggregate bump under optimistic concurrency.
	if err := s.bumpAggregate(ctx, in, now); err != nil {
		return SubmitResult{}, err
	}
	if in.Term.Scheme == vocab.FolkScheme {
		// Folk-term use count is a moderation signal only.
		s.bumpFolkUse(ctx, in.Term)
	}

	// Per-work velocity signal (abuse dashboards).
	_, _ = s.db.Increment(ctx, store.Key{PK: "VEL#WORK#" + in.WorkID, SK: "HOUR#" + now.Format("2006010215")}, 1, now.Add(24*time.Hour))

	// Dispute reconciliation is read-after-write and self-healing: every
	// subsequent vote re-runs it, so a racing pair converges.
	disputed, err := s.reconcileDispute(ctx, in.WorkID, in.Term)
	if err != nil {
		return result, nil //nolint:nilerr // the vote landed; marking heals later
	}
	result.Disputed = disputed
	return result, nil
}

// resolveTerm validates a controlled term against the vocabulary index or
// runs a folk term through normalization and its lifecycle gate. Returns the
// canonicalized ref and whether a novel folk term was just proposed.
func (s *Service) resolveTerm(ctx context.Context, ref vocab.TermRef, patron bool) (vocab.TermRef, bool, error) {
	// The patron-suggestion policy applies only to the anonymous
	// intake (Submit). A cataloger path (ManualTerm) passes patron=false and is
	// never gated -- the cataloger is the authority, the policy is the public
	// intake gate.
	var pol Policy
	if patron {
		var err error
		if pol, err = s.GetPolicy(ctx); err != nil {
			return ref, false, err
		}
		if !pol.Enabled {
			return ref, false, ErrSuggestionsOff
		}
	}
	if ref.Scheme != vocab.FolkScheme {
		if patron && !pol.allowsScheme(ref.Scheme) {
			return ref, false, ErrSchemeNotAllowed
		}
		if s.vocab == nil {
			return ref, false, ErrBadTerm
		}
		term, ok := s.vocab.Lookup(ref.Scheme, ref.ID)
		if !ok {
			return ref, false, ErrBadTerm
		}
		ref.Label = term.Label("")
		return ref, false, nil
	}
	if patron && pol.FreeText == FreeTextOff {
		return ref, false, ErrFreeTextOff
	}
	norm, err := vocab.NormalizeFolk(ref.ID)
	if err != nil {
		return ref, false, ErrBadTerm
	}
	ref.ID = norm
	ref.Label = norm
	rec, err := s.db.Get(ctx, folkKey(norm))
	switch {
	case errors.Is(err, store.ErrNotFound):
		// A novel folk tag: refused when the policy admits only tags already in
		// use, before it is created.
		if patron && pol.FreeText == FreeTextExisting {
			return ref, false, ErrNovelTagOff
		}
		ft := FolkTerm{Term: norm, Status: FolkProposed, CreatedAt: s.now().UTC()}
		data, _ := json.Marshal(ft)
		if _, err := s.db.Put(ctx, store.Record{Key: folkKey(norm), Data: data}, store.CondIfAbsent); err != nil && !errors.Is(err, store.ErrConditionFailed) {
			return ref, false, err
		}
		return ref, true, nil
	case err != nil:
		return ref, false, err
	}
	var ft FolkTerm
	if err := json.Unmarshal(rec.Data, &ft); err != nil {
		return ref, false, err
	}
	if ft.Status == FolkBlocked {
		return ref, false, ErrFolkBlocked
	}
	return ref, false, nil
}

func (s *Service) bumpFolkUse(ctx context.Context, ref vocab.TermRef) {
	_ = s.mutateFolk(ctx, ref.ID, func(ft *FolkTerm) { ft.UseCount++ })
}

// mutateFolk applies mutate to a folk term record under optimistic
// concurrency.
func (s *Service) mutateFolk(ctx context.Context, norm string, mutate func(*FolkTerm)) error {
	return s.casUpdate(ctx, folkKey(norm), "suggest: folk update conflict", false, func(data []byte, _ bool) ([]byte, error) {
		var ft FolkTerm
		if err := json.Unmarshal(data, &ft); err != nil {
			return nil, err
		}
		mutate(&ft)
		return json.Marshal(ft)
	})
}

// casRetries bounds optimistic-concurrency retry loops; contention on a hot
// aggregate is short-lived, so back off briefly between attempts.
const casRetries = 24

func casBackoff(attempt int) {
	if attempt > 2 {
		time.Sleep(time.Duration(attempt) * time.Millisecond)
	}
}

// casUpdate is the shared optimistic-concurrency loop behind every suggest
// mutator: read the record at key, hand its bytes to apply, write the result
// back under the record's version, and retry from fresh on a lost race.
// apply decodes, mutates, and re-encodes (an error from it aborts the loop
// verbatim, so domain short-circuits like errAlreadyResolved ride through);
// found is false only when allowCreate is set and the key does not exist
// yet, making the write a create. conflict is the give-up error after
// casRetries lost races. Post-write side effects (status-index upkeep) stay
// with the caller: apply captures what they need, and casUpdate returning
// nil means the capturing attempt won.
func (s *Service) casUpdate(ctx context.Context, key store.Key, conflict string, allowCreate bool, apply func(data []byte, found bool) ([]byte, error)) error {
	for attempt := range casRetries {
		casBackoff(attempt)
		rec, err := s.db.Get(ctx, key)
		found := err == nil
		switch {
		case errors.Is(err, store.ErrNotFound) && allowCreate:
			rec = store.Record{Key: key}
		case err != nil:
			return err
		}
		data, err := apply(rec.Data, found)
		if err != nil {
			return err
		}
		rec.Data = data
		if _, err := s.db.Put(ctx, rec, store.CondIfVersion); err == nil {
			return nil
		} else if !errors.Is(err, store.ErrConditionFailed) {
			return err
		}
	}
	return errors.New(conflict)
}

// bumpAggregate creates or updates the (work, term, type) aggregate and its
// status index item.
func (s *Service) bumpAggregate(ctx context.Context, in SubmitInput, now time.Time) error {
	key := store.Key{PK: workPK(in.WorkID), SK: suggSK(in.Term, in.Type)}
	var status Status
	err := s.casUpdate(ctx, key, "suggest: aggregate update conflict", true, func(data []byte, found bool) ([]byte, error) {
		sg := Suggestion{
			WorkID:     in.WorkID,
			Term:       in.Term,
			Type:       in.Type,
			Status:     StatusPending,
			Provenance: ProvenancePatron,
			WorkTitle:  in.WorkTitle,
			SourceRef:  in.SourceRef,
			CreatedAt:  now,
		}
		if found {
			var err error
			if sg, err = unmarshalSuggestion(data); err != nil {
				return nil, err
			}
		}
		sg.SupporterCount++
		sg.LastActivityAt = now
		if in.Type == TypeRemove {
			if sg.ReasonCounts == nil {
				sg.ReasonCounts = map[Reason]int{}
			}
			sg.ReasonCounts[in.Reason]++
		}
		status = sg.Status
		return marshalSuggestion(sg)
	})
	if err != nil {
		return err
	}
	s.writeStatusIndex(ctx, status, key)
	return nil
}

// writeStatusIndex mirrors an aggregate into its status partition
// (best-effort; hydration self-heals stale items).
func (s *Service) writeStatusIndex(ctx context.Context, status Status, aggKey store.Key) {
	data, _ := json.Marshal(aggKey)
	_, _ = s.db.Put(ctx, store.Record{Key: statusIndexKey(status, aggKey), Data: data}, store.CondNone)
}

// moveStatusIndex retires the old partition's item and writes the new one.
func (s *Service) moveStatusIndex(ctx context.Context, from, to Status, aggKey store.Key) {
	if from == to {
		return
	}
	_ = s.db.Delete(ctx, store.Record{Key: statusIndexKey(from, aggKey)}, store.CondNone)
	s.writeStatusIndex(ctx, to, aggKey)
}

// reconcileDispute flips both sides of a (work, term) pair to DISPUTED when
// ADD and REMOVE pressure coexist while either side is still open.
func (s *Service) reconcileDispute(ctx context.Context, workID string, term vocab.TermRef) (bool, error) {
	var open [2]bool
	types := [2]SuggType{TypeAdd, TypeRemove}
	for i, t := range types {
		rec, err := s.db.Get(ctx, store.Key{PK: workPK(workID), SK: suggSK(term, t)})
		if errors.Is(err, store.ErrNotFound) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		sg, err := unmarshalSuggestion(rec.Data)
		if err != nil {
			return false, err
		}
		open[i] = sg.Status == StatusPending || sg.Status == StatusDisputed
	}
	if !open[0] || !open[1] {
		// A resolved counterpart stays resolved; the open side still
		// surfaces in the queue with prior context.
		return false, nil
	}
	for _, t := range types {
		if err := s.transition(ctx, store.Key{PK: workPK(workID), SK: suggSK(term, t)}, StatusDisputed, func(sg *Suggestion) {}); err != nil {
			return false, err
		}
	}
	return true, nil
}

// transition moves an open aggregate to a new status under optimistic
// concurrency, keeping the status index in step. Resolved items are left
// alone (concurrent-reviewer safe).
func (s *Service) transition(ctx context.Context, key store.Key, to Status, stamp func(*Suggestion)) error {
	var from Status
	err := s.casUpdate(ctx, key, "suggest: transition conflict", false, func(data []byte, _ bool) ([]byte, error) {
		sg, err := unmarshalSuggestion(data)
		if err != nil {
			return nil, err
		}
		if sg.Status != StatusPending && sg.Status != StatusDisputed {
			return nil, errAlreadyResolved
		}
		from = sg.Status
		sg.Status = to
		stamp(&sg)
		return marshalSuggestion(sg)
	})
	if err != nil {
		return err
	}
	s.moveStatusIndex(ctx, from, to, key)
	return nil
}

var errAlreadyResolved = errors.New("suggest: already resolved")

// rejectApprovedUnpublished moves an APPROVED aggregate that never published
// to REJECTED -- the only exit for a row whose work is gone (the publisher
// skips it every run and transition refuses resolved rows). A row that DID
// publish stays resolved: undoing it means editing the graph, not the queue.
func (s *Service) rejectApprovedUnpublished(ctx context.Context, key store.Key, stamp func(*Suggestion)) error {
	err := s.casUpdate(ctx, key, "suggest: transition conflict", false, func(data []byte, _ bool) ([]byte, error) {
		sg, err := unmarshalSuggestion(data)
		if err != nil {
			return nil, err
		}
		if sg.Status != StatusApproved || sg.PublishedETag != "" {
			return nil, errAlreadyResolved
		}
		sg.Status = StatusRejected
		stamp(&sg)
		return marshalSuggestion(sg)
	})
	if err != nil {
		return err
	}
	s.moveStatusIndex(ctx, StatusApproved, StatusRejected, key)
	return nil
}

// RejectOpenForWork closes every open (PENDING/DISPUTED) aggregate for a
// work with a moderator-grade reject -- the tombstone path calls it so
// retiring a work clears its queue noise in one motion, audit-stamped.
// Resolved rows are left alone; the count says how many closed.
func (s *Service) RejectOpenForWork(ctx context.Context, workID, actor, note string) (int, error) {
	now := s.now().UTC()
	closed := 0
	var terms []string
	for rec, err := range s.db.Query(ctx, workPK(workID), "SUGG#", store.QueryOpt{}) {
		if err != nil {
			return closed, err
		}
		sg, err := unmarshalSuggestion(rec.Data)
		if err != nil || (sg.Status != StatusPending && sg.Status != StatusDisputed) {
			continue
		}
		err = s.transition(ctx, rec.Key, StatusRejected, func(cur *Suggestion) {
			cur.ReviewedAt = now
			cur.ReviewedBy = actor
			cur.ReviewNote = note
		})
		if errors.Is(err, errAlreadyResolved) {
			continue
		}
		if err != nil {
			return closed, err
		}
		closed++
		terms = append(terms, sg.Term.Scheme+":"+sg.Term.ID)
	}
	if closed > 0 {
		s.writeAudit(ctx, AuditEntry{
			WorkID: workID, Action: "WORK_TOMBSTONE_REJECT", Actor: actor,
			Terms: terms, Note: note,
		})
	}
	return closed, nil
}

// ForWork returns the public per-work view: aggregates only, no supporter
// hashes, for the "N patrons suggested this" display.
func (s *Service) ForWork(ctx context.Context, workID string) ([]Suggestion, error) {
	var out []Suggestion
	for rec, err := range s.db.Query(ctx, workPK(workID), "SUGG#", store.QueryOpt{}) {
		if err != nil {
			return nil, err
		}
		sg, err := unmarshalSuggestion(rec.Data)
		if err != nil {
			continue
		}
		out = append(out, sg)
	}
	return out, nil
}

// FolkTermStatus returns a folk term's lifecycle record.
func (s *Service) FolkTermStatus(ctx context.Context, norm string) (FolkTerm, error) {
	rec, err := s.db.Get(ctx, folkKey(norm))
	if err != nil {
		return FolkTerm{}, err
	}
	var ft FolkTerm
	if err := json.Unmarshal(rec.Data, &ft); err != nil {
		return FolkTerm{}, err
	}
	return ft, nil
}

// AcceptedFolkTerms lists ACCEPTED folk terms matching prefix -- merged into
// autocomplete beside controlled vocabularies.
func (s *Service) AcceptedFolkTerms(ctx context.Context, prefix string, limit int) ([]string, error) {
	var out []string
	for rec, err := range s.db.Query(ctx, "FOLKIDX", "TERM#"+prefix, store.QueryOpt{Limit: limit}) {
		if err != nil {
			return nil, err
		}
		out = append(out, string(rec.Data))
	}
	return out, nil
}

func validReason(r Reason) bool {
	return slices.Contains(Reasons, r)
}
