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
	"io"
	"sort"
	"sync"
	"time"

	codex "github.com/freeeve/libcodex"
	"github.com/freeeve/libcodex/sru"
	"github.com/freeeve/libcodex/z3950"

	"github.com/freeeve/libcatalog/bibframe"
	"github.com/freeeve/libcatalog/identity"
	"github.com/freeeve/libcatalog/ingest"
	"github.com/freeeve/libcatalog/ingest/marc"
	"github.com/freeeve/libcatalog/storage/blob"

	"github.com/freeeve/libcatalog/backend/marcview"
	"github.com/freeeve/libcatalog/backend/store"
	"github.com/freeeve/libcatalog/backend/suggest"
	"github.com/freeeve/libcatalog/backend/trigger"
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

// Target is one external search source.
type Target struct {
	Name string `json:"name"`
	// URL: an SRU base URL, or a Z39.50 "host:port/database" target.
	URL      string `json:"url"`
	Protocol string `json:"protocol"`
	// SRU dialect knobs, all optional (Z39.50 targets ignore them).
	// Version is the SRU protocol version ("" = the client default, 1.2);
	// DNB, for one, only answers 1.1.
	Version string `json:"version,omitempty"`
	// Schema is the recordSchema requested ("" = marcxml); servers name
	// their MARC21 XML schema differently (DNB: "MARC21-xml").
	Schema string `json:"schema,omitempty"`
	// Indexes overrides the CQL index an access point maps to, for servers
	// off the Dublin Core / Bath mapping (K10plus: {"isbn": "pica.isb"}).
	Indexes map[string]string `json:"indexes,omitempty"`
}

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

// SearchResult is one external hit, ready to stage.
type SearchResult struct {
	Target  string             `json:"target"`
	Title   string             `json:"title,omitempty"`
	Author  string             `json:"author,omitempty"`
	Date    string             `json:"date,omitempty"`
	ISBN    string             `json:"isbn,omitempty"`
	Edition string             `json:"edition,omitempty"`
	LCCN    string             `json:"lccn,omitempty"`
	Record  marcview.RecordDoc `json:"record"`
}

// FieldTerm is one (access point, term) pair of a fielded search; terms AND
// together. Indexes are the ones libcodex maps on both protocols.
type FieldTerm struct {
	Index string `json:"index"`
	Term  string `json:"term"`
}

// searchIndexes are the access points supported on both protocols: bib-1 use
// attributes on Z39.50, CQL indexes on SRU (lccn via the Bath profile).
var searchIndexes = map[string]bool{
	"any": true, "title": true, "author": true, "subject": true,
	"isbn": true, "issn": true, "lccn": true, "id": true,
}

// SearchFunc is the protocol seam: it fetches up to limit records from one
// target. Tests inject fakes; production uses protocolSearch.
type SearchFunc func(ctx context.Context, t Target, terms []FieldTerm, limit int) ([]*codex.Record, error)

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
}

func (s *Service) feed() string {
	if s.Feed == "" {
		return "copycat"
	}
	return s.Feed
}

func mintID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func targetKey(name string) store.Key { return store.Key{PK: "COPYCAT", SK: "T#" + name} }
func seededKey() store.Key            { return store.Key{PK: "COPYCAT", SK: "SEEDED"} }
func batchKey(id string) store.Key    { return store.Key{PK: "COPYCAT", SK: "B#" + id} }
func recordKey(batchID string, i int) store.Key {
	return store.Key{PK: "CCREC#" + batchID, SK: fmt.Sprintf("R#%06d", i)}
}

// DefaultTargets are the search sources seeded on a store that has never had
// targets: open, anonymous SRU endpoints serving MARC21, each verified live
// against the exact queries the fielded search emits (tasks/074, tasks/087).
// LOC speaks the Bath-profile identifier indexes as-is; DNB only answers SRU
// 1.1 and names its schema MARC21-xml, with dnb.num covering both standard
// numbers; K10plus wants its PICA identifier indexes.
var DefaultTargets = []Target{
	{Name: "dnb-sru", URL: "https://services.dnb.de/sru/dnb", Protocol: ProtocolSRU,
		Version: "1.1", Schema: "MARC21-xml",
		Indexes: map[string]string{"isbn": "dnb.num", "issn": "dnb.num"}},
	{Name: "k10plus-sru", URL: "https://sru.k10plus.de/opac-de-627", Protocol: ProtocolSRU,
		Indexes: map[string]string{"isbn": "pica.isb", "issn": "pica.iss"}},
	{Name: "loc-sru", URL: "http://lx2.loc.gov:210/LCDB", Protocol: ProtocolSRU},
}

// SeedDefaultTargets installs DefaultTargets so a fresh deployment's subject
// lookup and copy cataloging work without configuration. It runs once ever
// per store (a marker record remembers the seeding), so an admin who
// deletes every target stays at zero across restarts.
func (s *Service) SeedDefaultTargets(ctx context.Context) error {
	if _, err := s.DB.Get(ctx, seededKey()); err == nil {
		return nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return err
	}
	targets, err := s.Targets(ctx)
	if err != nil {
		return err
	}
	if _, err := s.DB.Put(ctx, store.Record{Key: seededKey(), Data: []byte(`{}`)}, store.CondNone); err != nil {
		return err
	}
	if len(targets) > 0 {
		return nil
	}
	for _, t := range DefaultTargets {
		if err := s.PutTarget(ctx, t); err != nil {
			return err
		}
	}
	return nil
}

// PutTarget creates or replaces a search target.
func (s *Service) PutTarget(ctx context.Context, t Target) error {
	if t.Name == "" || t.URL == "" || (t.Protocol != ProtocolSRU && t.Protocol != ProtocolZ3950) {
		return fmt.Errorf("%w: a target needs a name, url, and protocol sru|z3950", ErrValidation)
	}
	data, err := json.Marshal(t)
	if err != nil {
		return err
	}
	_, err = s.DB.Put(ctx, store.Record{Key: targetKey(t.Name), Data: data}, store.CondNone)
	return err
}

// DeleteTarget removes a target.
func (s *Service) DeleteTarget(ctx context.Context, name string) error {
	err := s.DB.Delete(ctx, store.Record{Key: targetKey(name)}, store.CondNone)
	if errors.Is(err, store.ErrNotFound) {
		return ErrNotFound
	}
	return err
}

// Targets lists the configured sources, sorted by name.
func (s *Service) Targets(ctx context.Context) ([]Target, error) {
	out := []Target{}
	for rec, err := range s.DB.Query(ctx, "COPYCAT", "T#", store.QueryOpt{}) {
		if err != nil {
			return nil, err
		}
		var t Target
		if json.Unmarshal(rec.Data, &t) == nil {
			out = append(out, t)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// SearchAll fans the query out to every configured target (or the named
// subset) concurrently and returns the normalized hits; per-target failures
// come back as errors keyed by target name rather than failing the fan-out.
// A bare query searches the server-choice "any" index; fields AND onto it.
func (s *Service) SearchAll(ctx context.Context, query string, fields []FieldTerm, names []string) ([]SearchResult, map[string]string, error) {
	terms, err := searchTerms(query, fields)
	if err != nil {
		return nil, nil, err
	}
	targets, err := s.Targets(ctx)
	if err != nil {
		return nil, nil, err
	}
	if len(names) > 0 {
		want := map[string]bool{}
		for _, n := range names {
			want[n] = true
		}
		filtered := targets[:0]
		for _, t := range targets {
			if want[t.Name] {
				filtered = append(filtered, t)
			}
		}
		targets = filtered
	}
	if len(targets) == 0 {
		return nil, nil, fmt.Errorf("%w: no search targets configured", ErrValidation)
	}
	search := s.Search
	if search == nil {
		search = protocolSearch
	}
	ctx, cancel := context.WithTimeout(ctx, searchTimeout)
	defer cancel()

	var mu sync.Mutex
	var wg sync.WaitGroup
	results := []SearchResult{}
	failures := map[string]string{}
	for _, t := range targets {
		wg.Add(1)
		go func(t Target) {
			defer wg.Done()
			recs, err := search(ctx, t, terms, searchLimit)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				failures[t.Name] = err.Error()
				return
			}
			for _, rec := range recs {
				results = append(results, SearchResult{
					Target:  t.Name,
					Title:   rec.SubfieldValue("245", 'a'),
					Author:  rec.SubfieldValue("100", 'a'),
					Date:    rec.SubfieldValue("260", 'c') + rec.SubfieldValue("264", 'c'),
					ISBN:    rec.SubfieldValue("020", 'a'),
					Edition: rec.SubfieldValue("250", 'a'),
					LCCN:    rec.SubfieldValue("010", 'a'),
					Record:  marcview.RecordToDoc(rec),
				})
			}
		}(t)
	}
	wg.Wait()
	sort.SliceStable(results, func(i, j int) bool { return results[i].Target < results[j].Target })
	return results, failures, nil
}

// searchTerms normalizes a request into the ANDed term list: a bare query
// becomes an "any" term, fields append after it, indexes must be supported.
func searchTerms(query string, fields []FieldTerm) ([]FieldTerm, error) {
	terms := []FieldTerm{}
	if query != "" {
		terms = append(terms, FieldTerm{Index: "any", Term: query})
	}
	for _, ft := range fields {
		if !searchIndexes[ft.Index] {
			return nil, fmt.Errorf("%w: unknown search index %q", ErrValidation, ft.Index)
		}
		if ft.Term == "" {
			return nil, fmt.Errorf("%w: empty term for index %q", ErrValidation, ft.Index)
		}
		terms = append(terms, ft)
	}
	if len(terms) == 0 {
		return nil, fmt.Errorf("%w: empty query", ErrValidation)
	}
	return terms, nil
}

// protocolSearch is the production SearchFunc over the libcodex clients.
func protocolSearch(ctx context.Context, t Target, terms []FieldTerm, limit int) ([]*codex.Record, error) {
	switch t.Protocol {
	case ProtocolSRU:
		return sruSearch(ctx, t, terms, limit)
	case ProtocolZ3950:
		rd := z3950.NewClient(t.URL).NewReader(ctx, z3950Query(terms))
		defer rd.Close()
		return readUpTo(rd.Read, limit)
	}
	return nil, fmt.Errorf("%w: unknown protocol %q", ErrValidation, t.Protocol)
}

// sruSearch streams up to limit records through the libcodex SRU Reader with
// the target's dialect applied (protocol version, recordSchema, index map).
func sruSearch(ctx context.Context, t Target, terms []FieldTerm, limit int) ([]*codex.Record, error) {
	c := sru.NewClient(t.URL)
	c.Version = t.Version
	c.Schema = t.Schema
	rd := c.NewReader(ctx, sruQuery(t, terms).String())
	return readUpTo(rd.Read, limit)
}

// sruQuery assembles the ANDed CQL query. Dublin Core defines no identifier
// indexes, so isbn/issn/lccn go out as the Bath profile's bath.* access
// points -- LOC's SRU server rejects dc.isbn/dc.issn with "Unsupported index".
// A target's Indexes map overrides that per access point for servers with
// their own context sets.
func sruQuery(t Target, terms []FieldTerm) sru.Query {
	q := sru.Term(sruIndex(t, terms[0].Index), terms[0].Term)
	for _, ft := range terms[1:] {
		q = sru.And(q, sru.Term(sruIndex(t, ft.Index), ft.Term))
	}
	return q
}

func sruIndex(t Target, index string) string {
	if idx, ok := t.Indexes[index]; ok {
		return idx
	}
	switch index {
	case "isbn", "issn", "lccn":
		return "bath." + index
	}
	return index
}

// z3950Query assembles the ANDed RPN query. A lone free-text term keeps the
// pre-fielded word structure; everything else takes libcodex's automatic
// word/phrase choice.
func z3950Query(terms []FieldTerm) z3950.Query {
	q := z3950.Term(terms[0].Index, terms[0].Term)
	if len(terms) == 1 && terms[0].Index == "any" {
		q = q.Word()
	}
	for _, ft := range terms[1:] {
		q = z3950.And(q, z3950.Term(ft.Index, ft.Term))
	}
	return q
}

func readUpTo(read func() (*codex.Record, error), limit int) ([]*codex.Record, error) {
	var out []*codex.Record
	for len(out) < limit {
		rec, err := read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			// Partial results beat none: a mid-stream error after hits is
			// swallowed; an immediate error surfaces.
			if len(out) > 0 {
				break
			}
			return nil, err
		}
		out = append(out, rec)
	}
	return out, nil
}

// matchRecords dry-runs the identity resolver over the docs against the
// current corpus: a throwaway resolver seeded from the grain tree, so
// staging never mutates anything.
func (s *Service) matchRecords(ctx context.Context, docs []marcview.RecordDoc) ([]StagedRecord, error) {
	prior, _, err := bibframe.LoadPriorStore(ctx, s.Blob, s.Prefix+"data/works/", s.feed())
	if err != nil {
		return nil, err
	}
	r := identity.NewResolver()
	identity.SeedResolver(r, prior.Grains)
	for _, m := range prior.Merges {
		r.SeedMerge(m.From, m.To)
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
	// Re-match against the corpus as it is now (staging may be stale).
	docs := make([]marcview.RecordDoc, len(records))
	for i, sr := range records {
		docs[i] = sr.Record
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
