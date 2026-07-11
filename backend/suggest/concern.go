package suggest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/freeeve/libcat/backend/store"
	"github.com/freeeve/libcat/backend/vocab"
)

// Concern note bounds: long enough to say something, short enough to read
// in a queue row.
const (
	concernMinLen = 10
	concernMaxLen = 2000
)

// ConcernScheme is the pseudo-scheme concern ids ride in Term -- reusing
// the suggestion aggregate's storage/queue/review machinery without a
// parallel record type. A concern never resolves against a
// vocabulary and never publishes.
const ConcernScheme = "concern"

// SubmitConcern records an anonymous report-a-problem against a work: the
// freetext lands in Note, moderation happens in the same queue as term
// suggestions (resolve/dismiss), and nothing ever reaches the graph. The
// concern id is a content hash, so an identical resubmission is an
// idempotent no-op; rate caps are shared with term suggestions.
func (s *Service) SubmitConcern(ctx context.Context, workID, note, workTitle, supporterHash string) error {
	note = strings.TrimSpace(note)
	if len(note) < concernMinLen || len(note) > concernMaxLen {
		return fmt.Errorf("suggest: a concern needs %d-%d characters", concernMinLen, concernMaxLen)
	}
	now := s.now().UTC()
	if err := s.bumpRate(ctx, supporterHash, now); err != nil {
		return err
	}
	sum := sha256.Sum256([]byte(workID + "\x00" + note))
	term := vocab.TermRef{Scheme: ConcernScheme, ID: hex.EncodeToString(sum[:8])}
	sg := Suggestion{
		WorkID: workID, Term: term, Type: TypeConcern,
		Status: StatusPending, Provenance: ProvenancePatron,
		Note: note, WorkTitle: workTitle, SupporterCount: 1,
		CreatedAt: now, LastActivityAt: now,
	}
	data, err := marshalSuggestion(sg)
	if err != nil {
		return err
	}
	key := store.Key{PK: workPK(workID), SK: suggSK(term, TypeConcern)}
	if _, err := s.db.Put(ctx, store.Record{Key: key, Data: data}, store.CondIfAbsent); err != nil {
		if errors.Is(err, store.ErrConditionFailed) {
			return nil // identical concern already filed; moderation owns it
		}
		return err
	}
	s.writeStatusIndex(ctx, StatusPending, key)
	return nil
}
