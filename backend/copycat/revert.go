package copycat

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/freeeve/libcatalog/bibframe"
	"github.com/freeeve/libcatalog/storage/blob"

	"github.com/freeeve/libcatalog/backend/store"
	"github.com/freeeve/libcatalog/backend/suggest"
	"github.com/freeeve/libcatalog/backend/trigger"
)

// StatusReverted marks a committed batch whose grains were rolled back.
const StatusReverted = "REVERTED"

// revertRecord is one changed grain's pre/post-commit capture (tasks/068):
// enough to restore the prior bytes exactly, or the reason it cannot revert.
type revertRecord struct {
	Path string `json:"path"`
	// Prior is the grain's pre-commit bytes (absent for created grains).
	Prior []byte `json:"prior,omitempty"`
	// Created marks a grain the commit minted; revert tombstones it.
	Created bool `json:"created,omitempty"`
	// PostHash fingerprints the post-commit bytes -- a mismatch at revert
	// time means someone edited the grain since, so it is skipped.
	PostHash string `json:"postHash,omitempty"`
	// Skip records why this grain can never revert (captured at commit).
	Skip string `json:"skip,omitempty"`
}

// RevertSkip reports one grain the revert left alone.
type RevertSkip struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

// RevertResult summarizes a batch revert.
type RevertResult struct {
	Batch    Batch        `json:"batch"`
	Reverted int          `json:"reverted"`
	Skipped  []RevertSkip `json:"skipped,omitempty"`
}

func revertKey(batchID, grainPath string) store.Key {
	return store.Key{PK: "CCREV#" + batchID, SK: grainPath}
}

func grainHash(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// preCommitSnapshot captures the state the revert path needs before the
// pipeline runs: the set of existing grain paths (so created grains are
// recognizable afterwards) and the bytes of every matched work grain (the
// overlay candidates).
func (s *Service) preCommitSnapshot(ctx context.Context, fresh []StagedRecord) (map[string]bool, map[string][]byte, error) {
	existed := map[string]bool{}
	for entry, err := range s.Blob.List(ctx, s.Prefix+"data/works/") {
		if err != nil {
			return nil, nil, err
		}
		if strings.HasSuffix(entry.Path, ".nq") {
			existed[entry.Path] = true
		}
	}
	priors := map[string][]byte{}
	for _, sr := range fresh {
		if !sr.Match.MatchedWork || sr.Match.WorkID == "" {
			continue
		}
		p := s.Prefix + bibframe.GrainPath(sr.Match.WorkID)
		if _, ok := priors[p]; ok {
			continue
		}
		data, _, err := s.Blob.Get(ctx, p)
		if err != nil {
			continue // stale match; the pipeline re-resolves anyway
		}
		priors[p] = data
	}
	return existed, priors, nil
}

// writeRevertSet records the commit's grain-level undo information. A
// re-commit replaces the previous set wholesale.
func (s *Service) writeRevertSet(ctx context.Context, batchID string, changed []string, existed map[string]bool, priors map[string][]byte) error {
	for rec, err := range s.DB.Query(ctx, "CCREV#"+batchID, "", store.QueryOpt{}) {
		if err != nil {
			return err
		}
		_ = s.DB.Delete(ctx, store.Record{Key: rec.Key}, store.CondNone)
	}
	for _, p := range changed {
		rr := revertRecord{Path: p}
		post, _, err := s.Blob.Get(ctx, p)
		switch {
		case err != nil:
			rr.Skip = "grain removed by the pipeline (merge retirement)"
		case !existed[p]:
			rr.Created = true
			rr.PostHash = grainHash(post)
		default:
			prior, ok := priors[p]
			if !ok {
				rr.Skip = "pre-commit state not captured (pipeline side effect)"
			} else {
				rr.Prior = prior
				rr.PostHash = grainHash(post)
			}
		}
		data, err := json.Marshal(rr)
		if err != nil {
			return err
		}
		if _, err := s.DB.Put(ctx, store.Record{Key: revertKey(batchID, p), Data: data}, store.CondNone); err != nil {
			return err
		}
	}
	return nil
}

// Revert rolls a committed batch back grain by grain: overlaid grains get
// their pre-commit bytes restored exactly (byte-stable), created grains are
// tombstoned (URL stability over deletion), and any grain whose post-commit
// state carries later edits is skipped and reported -- editorial work is
// never destroyed by an undo.
func (s *Service) Revert(ctx context.Context, id, actor string) (RevertResult, error) {
	b, _, err := s.GetBatch(ctx, id)
	if err != nil {
		return RevertResult{}, err
	}
	if b.Status != StatusCommitted {
		return RevertResult{}, fmt.Errorf("%w: batch %s is %s; only a committed batch reverts", ErrValidation, id, b.Status)
	}
	var recs []revertRecord
	for rec, err := range s.DB.Query(ctx, "CCREV#"+id, "", store.QueryOpt{}) {
		if err != nil {
			return RevertResult{}, err
		}
		var rr revertRecord
		if json.Unmarshal(rec.Data, &rr) == nil {
			recs = append(recs, rr)
		}
	}
	if len(recs) == 0 && b.Committed > 0 {
		return RevertResult{}, fmt.Errorf("%w: batch %s has no recorded commit set (committed before revert support?)", ErrValidation, id)
	}
	result := RevertResult{}
	changed := []string{}
	for _, rr := range recs {
		if rr.Skip != "" {
			result.Skipped = append(result.Skipped, RevertSkip{Path: rr.Path, Reason: rr.Skip})
			continue
		}
		current, etag, err := s.Blob.Get(ctx, rr.Path)
		if err != nil {
			result.Skipped = append(result.Skipped, RevertSkip{Path: rr.Path, Reason: "grain missing"})
			continue
		}
		if grainHash(current) != rr.PostHash {
			result.Skipped = append(result.Skipped, RevertSkip{Path: rr.Path, Reason: "edited after commit"})
			continue
		}
		next := rr.Prior
		if rr.Created {
			workID := strings.TrimSuffix(path.Base(rr.Path), ".nq")
			next, err = bibframe.SetTombstone(current, workID, "")
			if err != nil {
				result.Skipped = append(result.Skipped, RevertSkip{Path: rr.Path, Reason: "tombstone failed: " + err.Error()})
				continue
			}
		}
		if _, err := s.Blob.Put(ctx, rr.Path, next, blob.PutOptions{IfMatch: etag, ContentType: "application/n-quads"}); err != nil {
			result.Skipped = append(result.Skipped, RevertSkip{Path: rr.Path, Reason: "concurrent write"})
			continue
		}
		result.Reverted++
		changed = append(changed, rr.Path)
	}
	b.Status = StatusReverted
	b.Reverted = result.Reverted
	b.RevertAt = time.Now().UTC()
	if err := s.putBatch(ctx, b, store.CondNone); err != nil {
		return result, err
	}
	result.Batch = b
	s.audit(ctx, suggest.AuditEntry{
		Action: "COPYCAT_REVERT", Actor: actor,
		Note: fmt.Sprintf("batch %s: %d grains reverted, %d skipped", b.ID, result.Reverted, len(result.Skipped)),
	})
	if s.Trigger != nil && len(changed) > 0 {
		_ = s.Trigger.Notify(ctx, trigger.Event{Kind: "grains-changed", Paths: changed, At: time.Now().UTC()})
	}
	return result, nil
}
