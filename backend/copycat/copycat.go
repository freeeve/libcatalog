// Package copycat is Koha's Z39.50/SRU copy cataloging and staged-import
// workflow over the shared ingest pipeline (tasks/050): external targets are
// searched through the libcodex protocol clients, results and .mrc uploads
// stage into datastore batches (nothing touches the grain tree), every
// staged record carries its identity-resolver match ("would merge with Work
// w…"), and commit runs the batch through the same clustering pipeline every
// feed uses -- store-backed, CAS-guarded, editorial always preserved.
package copycat

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	codex "github.com/freeeve/libcodex"

	"github.com/freeeve/libcat/bibframe"
	"github.com/freeeve/libcat/identity"
	"github.com/freeeve/libcat/ingest"
	"github.com/freeeve/libcat/ingest/marc"
	"github.com/freeeve/libcat/storage/blob"

	"github.com/freeeve/libcat/backend/marcview"
	"github.com/freeeve/libcat/backend/store"
	"github.com/freeeve/libcat/backend/suggest"
	"github.com/freeeve/libcat/backend/trigger"
	"github.com/freeeve/libcat/backend/workindex"
)

// Protocols a target can speak.
const (
	ProtocolSRU   = "sru"
	ProtocolZ3950 = "z3950"
)

// Overlay policies: what a commit does with records that match the existing
// corpus. Editorial statements are preserved by the pipeline in every case.
const (
	// PolicyReplaceFeed commits everything; a matched Instance's feed
	// statements are replaced by the incoming record's (the pipeline's
	// re-ingest semantics).
	PolicyReplaceFeed = "replace-feed"
	// PolicyFillHoles commits only records whose Instance is new -- a
	// matched Work gains the new edition, but an existing Instance is never
	// overwritten.
	PolicyFillHoles = "fill-holes-only"
	// PolicyNever commits only records with no match at all.
	PolicyNever = "never"
)

// Decisions a reviewer takes per staged record.
const (
	DecisionImport = "import"
	DecisionSkip   = "skip"
)

// Batch statuses.
const (
	StatusStaged    = "STAGED"
	StatusCommitted = "COMMITTED"
)

// ErrValidation reports a request the service refuses.
var ErrValidation = errors.New("copycat: invalid request")

// ErrNotFound reports a missing target or batch.
var ErrNotFound = errors.New("copycat: not found")

const (
	maxBatchRecords = 1000
	searchLimit     = 20
	searchTimeout   = 15 * time.Second
)

// Match is a staged record's dry-run identity resolution against the
// current corpus.
type Match struct {
	WorkID          string `json:"workId,omitempty"`
	InstanceID      string `json:"instanceId,omitempty"`
	MatchedWork     bool   `json:"matchedWork"`
	MatchedInstance bool   `json:"matchedInstance"`
}

// StagedRecord is one record of a batch, reviewable before commit.
type StagedRecord struct {
	Index    int                `json:"index"`
	Record   marcview.RecordDoc `json:"record"`
	Title    string             `json:"title,omitempty"`
	Match    Match              `json:"match"`
	Decision string             `json:"decision"`
}

// Batch is one staged import.
type Batch struct {
	ID        string    `json:"id"`
	Label     string    `json:"label"`
	Source    string    `json:"source"` // "upload" or a target name
	Policy    string    `json:"policy"`
	Status    string    `json:"status"`
	Records   int       `json:"records"`
	Owner     string    `json:"owner"`
	CreatedAt time.Time `json:"createdAt"`
	// Commit outcome.
	Committed int       `json:"committed,omitempty"`
	Skipped   int       `json:"skipped,omitempty"`
	CommitAt  time.Time `json:"commitAt,omitzero"`
	// Revert outcome (tasks/068).
	Reverted int       `json:"reverted,omitempty"`
	RevertAt time.Time `json:"revertAt,omitzero"`
}

// Service is the copy-cataloging surface.
type Service struct {
	Blob blob.Store
	DB   store.Store
	// Queue receives audit entries (optional).
	Queue *suggest.Service
	// Trigger gets one grains-changed event per commit (optional).
	Trigger trigger.Notifier
	// Prefix roots the grain tree ("" = repo layout).
	Prefix string
	// Feed names the provenance graph committed batches write (default
	// "copycat").
	Feed string
	// Search overrides the protocol clients (tests); nil = protocolSearch.
	Search SearchFunc
	// Index, when set (and Prefix is ""), is the shared work index match
	// passes seed from instead of loading the whole prior grain store per
	// Stage/Commit (tasks/107); nil falls back to LoadPriorStore.
	Index *workindex.Index
}

func (s *Service) feed() string {
	if s.Feed == "" {
		return "copycat"
	}
	return s.Feed
}

// sharedIndex returns the work index when it actually covers this service's
// grain tree: the index is built on repo-layout paths, so a prefixed
// deployment falls back to its own loads.
func (s *Service) sharedIndex() *workindex.Index {
	if s.Prefix != "" {
		return nil
	}
	return s.Index
}

func mintID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func batchKey(id string) store.Key { return store.Key{PK: "COPYCAT", SK: "B#" + id} }
func recordKey(batchID string, i int) store.Key {
	return store.Key{PK: "CCREC#" + batchID, SK: fmt.Sprintf("R#%06d", i)}
}

// matchRecords dry-runs the identity resolver over the docs against the
// current corpus: a throwaway resolver seeded from the shared work index
// (or, without one, a full grain-tree load), so staging never mutates
// anything.
func (s *Service) matchRecords(ctx context.Context, docs []marcview.RecordDoc) ([]StagedRecord, error) {
	r := identity.NewResolver()
	if ix := s.sharedIndex(); ix != nil {
		if err := ix.SeedResolver(ctx, r); err != nil {
			return nil, err
		}
	} else {
		prior, _, err := bibframe.LoadPriorStore(ctx, s.Blob, s.Prefix+"data/works/", s.feed())
		if err != nil {
			return nil, err
		}
		identity.SeedResolver(r, prior.Grains)
		for _, m := range prior.Merges {
			r.SeedMerge(m.From, m.To)
		}
	}
	staged := make([]StagedRecord, 0, len(docs))
	for i, doc := range docs {
		rec, err := marcview.DocToRecord(doc)
		if err != nil {
			return nil, fmt.Errorf("record %d: %w", i, err)
		}
		a := r.Resolve(marc.Identity(rec))
		sr := StagedRecord{
			Index:    i,
			Record:   doc,
			Title:    rec.SubfieldValue("245", 'a'),
			Decision: DecisionImport,
			Match:    Match{MatchedWork: !a.MintedWork, MatchedInstance: !a.MintedInstance},
		}
		if sr.Match.MatchedWork {
			sr.Match.WorkID = a.WorkID
		}
		if sr.Match.MatchedInstance {
			sr.Match.InstanceID = a.InstanceID
		}
		staged = append(staged, sr)
	}
	return staged, nil
}

// Stage creates a batch from record docs (search imports or a parsed .mrc
// upload), each carrying its match banner.
func (s *Service) Stage(ctx context.Context, label, source string, docs []marcview.RecordDoc, owner string) (Batch, []StagedRecord, error) {
	if len(docs) == 0 || len(docs) > maxBatchRecords {
		return Batch{}, nil, fmt.Errorf("%w: a batch stages 1-%d records", ErrValidation, maxBatchRecords)
	}
	staged, err := s.matchRecords(ctx, docs)
	if err != nil {
		return Batch{}, nil, err
	}
	b := Batch{
		ID: mintID(), Label: label, Source: source, Policy: PolicyReplaceFeed,
		Status: StatusStaged, Records: len(staged), Owner: owner, CreatedAt: time.Now().UTC(),
	}
	if b.Label == "" {
		b.Label = b.Source + " " + b.CreatedAt.Format("2006-01-02 15:04")
	}
	if err := s.putBatch(ctx, b, store.CondIfAbsent); err != nil {
		return Batch{}, nil, err
	}
	for _, sr := range staged {
		data, err := json.Marshal(sr)
		if err != nil {
			return Batch{}, nil, err
		}
		if _, err := s.DB.Put(ctx, store.Record{Key: recordKey(b.ID, sr.Index), Data: data}, store.CondIfAbsent); err != nil {
			return Batch{}, nil, err
		}
	}
	s.audit(ctx, suggest.AuditEntry{
		Action: "COPYCAT_STAGE", Actor: owner,
		Note: fmt.Sprintf("batch %s (%s): %d records", b.ID, b.Source, b.Records),
	})
	return b, staged, nil
}

// StageMARC parses raw ISO 2709 bytes and stages them.
func (s *Service) StageMARC(ctx context.Context, label string, mrc []byte, owner string) (Batch, []StagedRecord, error) {
	recs, err := bibframe.ReadMARC(bytes.NewReader(mrc))
	if err != nil {
		return Batch{}, nil, fmt.Errorf("%w: %v", ErrValidation, err)
	}
	docs := make([]marcview.RecordDoc, 0, len(recs))
	for _, rec := range recs {
		docs = append(docs, marcview.RecordToDoc(rec))
	}
	return s.Stage(ctx, label, "upload", docs, owner)
}

func (s *Service) putBatch(ctx context.Context, b Batch, cond store.Cond) error {
	data, err := json.Marshal(b)
	if err != nil {
		return err
	}
	_, err = s.DB.Put(ctx, store.Record{Key: batchKey(b.ID), Data: data}, cond)
	return err
}

// Batches lists every staged import, newest first.
func (s *Service) Batches(ctx context.Context) ([]Batch, error) {
	out := []Batch{}
	for rec, err := range s.DB.Query(ctx, "COPYCAT", "B#", store.QueryOpt{}) {
		if err != nil {
			return nil, err
		}
		var b Batch
		if json.Unmarshal(rec.Data, &b) == nil {
			out = append(out, b)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

// GetBatch returns one batch with its records.
func (s *Service) GetBatch(ctx context.Context, id string) (Batch, []StagedRecord, error) {
	rec, err := s.DB.Get(ctx, batchKey(id))
	if errors.Is(err, store.ErrNotFound) {
		return Batch{}, nil, ErrNotFound
	}
	if err != nil {
		return Batch{}, nil, err
	}
	var b Batch
	if err := json.Unmarshal(rec.Data, &b); err != nil {
		return Batch{}, nil, err
	}
	records := []StagedRecord{}
	for r, err := range s.DB.Query(ctx, "CCREC#"+id, "R#", store.QueryOpt{}) {
		if err != nil {
			return Batch{}, nil, err
		}
		var sr StagedRecord
		if json.Unmarshal(r.Data, &sr) == nil {
			records = append(records, sr)
		}
	}
	return b, records, nil
}

// Review updates a batch's overlay policy and per-record decisions.
func (s *Service) Review(ctx context.Context, id, policy string, decisions map[int]string) (Batch, error) {
	b, records, err := s.GetBatch(ctx, id)
	if err != nil {
		return Batch{}, err
	}
	if b.Status != StatusStaged {
		return Batch{}, fmt.Errorf("%w: batch %s is %s", ErrValidation, id, b.Status)
	}
	if policy != "" {
		if policy != PolicyReplaceFeed && policy != PolicyFillHoles && policy != PolicyNever {
			return Batch{}, fmt.Errorf("%w: unknown policy %q", ErrValidation, policy)
		}
		b.Policy = policy
	}
	for idx, d := range decisions {
		if d != DecisionImport && d != DecisionSkip {
			return Batch{}, fmt.Errorf("%w: unknown decision %q", ErrValidation, d)
		}
		if idx < 0 || idx >= len(records) {
			return Batch{}, fmt.Errorf("%w: no record %d", ErrValidation, idx)
		}
		records[idx].Decision = d
		data, err := json.Marshal(records[idx])
		if err != nil {
			return Batch{}, err
		}
		if _, err := s.DB.Put(ctx, store.Record{Key: recordKey(id, idx), Data: data}, store.CondNone); err != nil {
			return Batch{}, err
		}
	}
	if err := s.putBatch(ctx, b, store.CondNone); err != nil {
		return Batch{}, err
	}
	return b, nil
}

// Commit runs a batch's importable records through the shared store-backed
// ingest pipeline: matches are re-resolved against the current corpus, the
// overlay policy filters, and grains land via CAS with editorial preserved.
// Committing an already-committed batch re-runs the same records -- the
// pipeline is byte-stable, so unchanged grains are untouched.
func (s *Service) Commit(ctx context.Context, id, actor string) (Batch, error) {
	b, records, err := s.GetBatch(ctx, id)
	if err != nil {
		return Batch{}, err
	}
	// Re-match against the corpus as it is now (staging may be stale). A
	// commit's match accuracy decides the overlay policy, so the shared
	// index is forced past its TTL first -- still an ETag diff, not a
	// corpus read.
	docs := make([]marcview.RecordDoc, len(records))
	for i, sr := range records {
		docs[i] = sr.Record
	}
	if ix := s.sharedIndex(); ix != nil {
		if err := ix.RefreshNow(ctx); err != nil {
			return Batch{}, err
		}
	}
	fresh, err := s.matchRecords(ctx, docs)
	if err != nil {
		return Batch{}, err
	}
	var commit []*codex.Record
	skipped := 0
	for i, sr := range records {
		m := fresh[i].Match
		keep := sr.Decision == DecisionImport
		switch b.Policy {
		case PolicyFillHoles:
			keep = keep && !m.MatchedInstance
		case PolicyNever:
			keep = keep && !m.MatchedWork && !m.MatchedInstance
		}
		if !keep {
			skipped++
			continue
		}
		rec, err := marcview.DocToRecord(sr.Record)
		if err != nil {
			return Batch{}, fmt.Errorf("record %d: %w", sr.Index, err)
		}
		commit = append(commit, rec)
	}
	// Snapshot the pre-commit state the revert path needs (tasks/068).
	existed, priors, err := s.preCommitSnapshot(ctx, fresh)
	if err != nil {
		return Batch{}, err
	}
	changed := []string{}
	if len(commit) > 0 {
		prov := staticProvider{name: s.feed(), recs: marc.FromCodexRecords(commit)}
		_, paths, err := ingest.RunStore(ctx, prov, s.Blob, s.Prefix)
		if err != nil {
			return Batch{}, err
		}
		changed = paths
		// Push the landed grains into the shared index so the editor's
		// duplicate and barcode checks see them without waiting out the TTL.
		if ix := s.sharedIndex(); ix != nil {
			if err := ix.Update(ctx, changed...); err != nil {
				return Batch{}, err
			}
			// One batched feed append for the whole commit (not one per grain),
			// so other containers see it without a List; best-effort.
			_ = ix.AppendFeed(ctx, changed...)
		}
	}
	if err := s.writeRevertSet(ctx, b.ID, changed, existed, priors); err != nil {
		return Batch{}, err
	}
	b.Status = StatusCommitted
	b.Committed = len(commit)
	b.Skipped = skipped
	b.CommitAt = time.Now().UTC()
	if err := s.putBatch(ctx, b, store.CondNone); err != nil {
		return Batch{}, err
	}
	s.audit(ctx, suggest.AuditEntry{
		Action: "COPYCAT_COMMIT", Actor: actor,
		Note: fmt.Sprintf("batch %s: %d committed, %d skipped (%s), %d grains touched",
			b.ID, b.Committed, b.Skipped, b.Policy, len(changed)),
	})
	if s.Trigger != nil && len(changed) > 0 {
		_ = s.Trigger.Notify(ctx, trigger.Event{Kind: "grains-changed", Paths: changed, At: time.Now().UTC()})
	}
	return b, nil
}

// DeleteBatch removes a batch, its records, and its revert set.
func (s *Service) DeleteBatch(ctx context.Context, id string) error {
	b, records, err := s.GetBatch(ctx, id)
	if err != nil {
		return err
	}
	for _, sr := range records {
		_ = s.DB.Delete(ctx, store.Record{Key: recordKey(id, sr.Index)}, store.CondNone)
	}
	for rec, err := range s.DB.Query(ctx, "CCREV#"+id, "", store.QueryOpt{}) {
		if err == nil {
			_ = s.DB.Delete(ctx, store.Record{Key: rec.Key}, store.CondNone)
		}
	}
	return s.DB.Delete(ctx, store.Record{Key: batchKey(b.ID)}, store.CondNone)
}

func (s *Service) audit(ctx context.Context, entry suggest.AuditEntry) {
	if s.Queue != nil {
		s.Queue.WriteAudit(ctx, entry)
	}
}

// staticProvider adapts an in-memory record list to the ingest Provider
// contract.
type staticProvider struct {
	name string
	recs []ingest.Record
}

func (p staticProvider) Name() string      { return p.name }
func (p staticProvider) Role() ingest.Role { return ingest.RoleIngest }
func (p staticProvider) Records(context.Context) ([]ingest.Record, error) {
	return p.recs, nil
}
