package copycat

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"sync"

	codex "github.com/freeeve/libcodex"
	"github.com/freeeve/libcodex/sru"
	"github.com/freeeve/libcodex/z3950"

	"github.com/freeeve/libcat/backend/marcview"
)

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
