// Sidecar index reader: serves one scheme from the artifacts
// BuildSidecar wrote, holding only the small structures resident -- the URI
// and identifier RRILs, the RRTI search router, and the RRSR offset index --
// while search postings and Term payloads range-fetch from the store on
// demand (a bounded cache absorbs the editor's hot set). Reads are lock-free
// except the cache. Unlike the map path, sidecar reads can fail (the store
// is remote): failures log and report a miss, never an invented term.
package vocab

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"

	rr "github.com/freeeve/roaringrange"

	"github.com/freeeve/libcat/storage/blob"
	"github.com/freeeve/libcat/storage/vocabsidecar"
)

// sidecarTermCacheCap bounds the materialized-Term cache; at the editor's
// pace this covers the working set, and eviction is wholesale for
// simplicity (the next fetch is one ranged read).
const sidecarTermCacheCap = 4096

// sidecarScheme is one artifact-backed scheme of a snapshot.
type sidecarScheme struct {
	scheme  string
	count   uint32
	uri     *rr.LookupIndex
	tiers   [identifierTiers]*rr.LookupIndex
	records *rr.RecordStore
	// searchIdx is the RRTI over normalized labels (posting IDs doc<<1|alt);
	// only its router FST is resident, postings range-read per query.
	searchIdx *rr.TermIndex

	mu    sync.Mutex
	cache map[uint32]*Term
}

// openSidecar loads a scheme's resident artifacts and wires the record
// store over ranged reads of the .bin blob.
func openSidecar(ctx context.Context, st blob.Store, prefix string, m *vocabsidecar.SidecarManifest) (*sidecarScheme, error) {
	resident := func(suffix string) ([]byte, error) {
		data, _, err := st.Get(ctx, vocabsidecar.Path(prefix, m.Scheme, suffix))
		if err != nil {
			return nil, fmt.Errorf("vocab: sidecar %s%s: %w", m.Scheme, suffix, err)
		}
		return data, nil
	}
	s := &sidecarScheme{scheme: m.Scheme, count: uint32(m.Terms), cache: map[uint32]*Term{}}

	uriData, err := resident(".uri.rril")
	if err != nil {
		return nil, err
	}
	if s.uri, err = rr.OpenLookup(bytes.NewReader(uriData)); err != nil {
		return nil, fmt.Errorf("vocab: sidecar %s uri lookup: %w", m.Scheme, err)
	}
	for k := range identifierTiers {
		data, err := resident(fmt.Sprintf(".id%d.rril", k+1))
		if err != nil {
			return nil, err
		}
		if s.tiers[k], err = rr.OpenLookup(bytes.NewReader(data)); err != nil {
			return nil, fmt.Errorf("vocab: sidecar %s id tier %d: %w", m.Scheme, k+1, err)
		}
	}
	searchRA, _, _, err := blob.ReaderAt(ctx, st, vocabsidecar.Path(prefix, m.Scheme, ".search.rrt"))
	if err != nil {
		return nil, fmt.Errorf("vocab: sidecar %s search: %w", m.Scheme, err)
	}
	if s.searchIdx, err = rr.OpenTermIndex(searchRA); err != nil {
		return nil, fmt.Errorf("vocab: sidecar %s search index: %w", m.Scheme, err)
	}
	idxData, err := resident(".rrsr.idx")
	if err != nil {
		return nil, err
	}
	binRA, _, _, err := blob.ReaderAt(ctx, st, vocabsidecar.Path(prefix, m.Scheme, ".rrsr.bin"))
	if err != nil {
		return nil, fmt.Errorf("vocab: sidecar %s records: %w", m.Scheme, err)
	}
	if s.records, err = rr.OpenRecordStore(bytes.NewReader(idxData), binRA); err != nil {
		return nil, fmt.Errorf("vocab: sidecar %s record store: %w", m.Scheme, err)
	}
	return s, nil
}

// term materializes one doc, through the cache.
func (s *sidecarScheme) term(doc uint32) (*Term, bool) {
	s.mu.Lock()
	if t, ok := s.cache[doc]; ok {
		s.mu.Unlock()
		return t, true
	}
	s.mu.Unlock()
	data, ok, err := s.records.Get(doc)
	if err != nil || !ok {
		s.miss("record", err)
		return nil, false
	}
	t := &Term{}
	if err := json.Unmarshal(data, t); err != nil {
		s.miss("record decode", err)
		return nil, false
	}
	s.store(doc, t)
	return t, true
}

// termsMany materializes docs with one coalesced ranged read.
func (s *sidecarScheme) termsMany(docs []uint32) map[uint32]*Term {
	out := make(map[uint32]*Term, len(docs))
	var missing []uint32
	s.mu.Lock()
	for _, d := range docs {
		if t, ok := s.cache[d]; ok {
			out[d] = t
		} else {
			missing = append(missing, d)
		}
	}
	s.mu.Unlock()
	if len(missing) == 0 {
		return out
	}
	recs, err := s.records.GetMany(missing)
	if err != nil {
		s.miss("records", err)
		return out
	}
	for d, data := range recs {
		t := &Term{}
		if err := json.Unmarshal(data, t); err != nil {
			s.miss("record decode", err)
			continue
		}
		out[d] = t
		s.store(d, t)
	}
	return out
}

func (s *sidecarScheme) store(doc uint32, t *Term) {
	s.mu.Lock()
	if len(s.cache) >= sidecarTermCacheCap {
		s.cache = map[uint32]*Term{}
	}
	s.cache[doc] = t
	s.mu.Unlock()
}

func (s *sidecarScheme) miss(what string, err error) {
	if err != nil {
		slog.Warn("vocab: sidecar read failed; treating as miss", "scheme", s.scheme, "what", what, "err", err)
	}
}

// lookup is the Lookup gate: URI -> term, retired terms included.
func (s *sidecarScheme) lookup(uri string) (*Term, bool) {
	docs, err := s.uri.Lookup(uri)
	if err != nil {
		s.miss("uri lookup", err)
		return nil, false
	}
	if len(docs) == 0 {
		return nil, false
	}
	return s.term(docs[0])
}

// tierMatch resolves one identifier tier; postings are doc-ordered, so the
// first doc is the scheme's smallest URI (buildMatch's within-scheme rule).
func (s *sidecarScheme) tierMatch(tier int, key string) (*Term, bool) {
	docs, err := s.tiers[tier].Lookup(key)
	if err != nil {
		s.miss("id lookup", err)
		return nil, false
	}
	if len(docs) == 0 {
		return nil, false
	}
	return s.term(docs[0])
}

// search implements prefix search with the map path's semantics: matched
// labels in norm order, deduped by term, live terms only (retired terms have
// no entries by construction). Labels sharing a term can collapse under the
// dedup, so a truncated term window that underfills the limit grows and
// retries; each pass range-reads only the dict blocks spanning the prefix
// plus the matched labels' postings.
func (s *sidecarScheme) search(q string, limit int) []*Term {
	return hitTerms(s.searchHits(q, limit))
}

// searchHits is search, carrying each result's matched label norm. A term's
// norm is the smallest of its labels matching the prefix, because the term
// dictionary yields labels in ascending order and the first hit wins the
// dedupe. That norm, then the term URI, is the map path's sort key too, so
// Search can merge the two backends into one ordering.
func (s *sidecarScheme) searchHits(q string, limit int) []searchHit {
	var docs []uint32
	var norms []string
	for termLimit := limit; ; termLimit *= 4 {
		tps, truncated, err := s.searchIdx.PrefixPostings(q, termLimit)
		if err != nil {
			s.miss("search", err)
			return nil
		}
		docs, norms = docs[:0], norms[:0]
		seen := map[uint32]bool{}
	terms:
		for _, tp := range tps {
			it := tp.Posting.Iterator()
			for it.HasNext() {
				d := it.Next() >> 1
				if seen[d] {
					continue
				}
				seen[d] = true
				docs = append(docs, d)
				norms = append(norms, tp.Term)
				if len(docs) >= limit {
					break terms
				}
			}
		}
		if len(docs) >= limit || !truncated {
			break
		}
	}
	byDoc := s.termsMany(docs)
	out := make([]searchHit, 0, len(docs))
	for i, d := range docs {
		if t, ok := byDoc[d]; ok {
			out = append(out, searchHit{term: t, norm: norms[i]})
		}
	}
	return out
}

// matchLabel implements the exact-normalized-label gate. The exact label, if
// indexed, is byte-lexicographically first among its own prefix matches, so
// one single-term prefix read resolves it.
func (s *sidecarScheme) matchLabel(q string) []LabelMatch {
	tps, _, err := s.searchIdx.PrefixPostings(q, 1)
	if err != nil {
		s.miss("label match", err)
		return nil
	}
	if len(tps) == 0 || tps[0].Term != q {
		return nil
	}
	var docs []uint32
	var alts []bool
	seen := map[uint32]bool{}
	it := tps[0].Posting.Iterator()
	for it.HasNext() {
		enc := it.Next()
		d := enc >> 1
		if seen[d] {
			continue
		}
		seen[d] = true
		docs = append(docs, d)
		alts = append(alts, enc&1 == 1)
	}
	byDoc := s.termsMany(docs)
	var out []LabelMatch
	for i, d := range docs {
		if t, ok := byDoc[d]; ok {
			out = append(out, LabelMatch{Term: t, Alt: alts[i]})
		}
	}
	return out
}

// all streams every term in doc (URI) order -- the management listing.
func (s *sidecarScheme) all() []*Term {
	docs := make([]uint32, s.count)
	for i := range docs {
		docs[i] = uint32(i)
	}
	const chunk = 8192
	out := make([]*Term, 0, s.count)
	for start := 0; start < len(docs); start += chunk {
		end := min(start+chunk, len(docs))
		byDoc := s.termsMany(docs[start:end])
		for _, d := range docs[start:end] {
			if t, ok := byDoc[d]; ok {
				out = append(out, t)
			}
		}
	}
	return out
}
