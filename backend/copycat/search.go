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

// ErrCapped reports that the search limit, not the result set, ended the
// stream: the target may hold more records than were returned.
var ErrCapped = errors.New("result set truncated at the search limit")

// PartialError reports a stream that broke after delivering records. It carries
// both, because a caller needs the hits and the reason the set is short.
//
// Total is what the target said its result set holds, or -1 when it never said
// (unknownTotal). "1 of 9 records arrived" is a different sentence from "1 record
// arrived", and only the first tells a cataloger how much they are missing.
//
// The zero value is inert rather than wrong: a PartialError exists only when at
// least one record arrived, so Total == 0 can never exceed Got and reads as "no
// total", the same as -1.
type PartialError struct {
	Got   int
	Total int
	Err   error
}

func (e *PartialError) Error() string {
	if e.Total > e.Got {
		return fmt.Sprintf("partial results: the stream broke after %d of %d record(s): %v", e.Got, e.Total, e.Err)
	}
	return fmt.Sprintf("partial results: the stream broke after %d record(s): %v", e.Got, e.Err)
}

func (e *PartialError) Unwrap() error { return e.Err }

// Incomplete reports whether err means "these records, but not all of them" --
// a partial stream or the search cap -- as opposed to an outright failure.
// A search returning records with an Incomplete error is a warning, never a
// failure: suppressing the hits would throw away the useful half.
func Incomplete(err error) bool {
	var partial *PartialError
	return errors.As(err, &partial) || errors.Is(err, ErrCapped)
}

// SearchFunc is the protocol seam: it fetches up to limit records from one
// target. A non-nil error alongside records means the set is incomplete; see
// Incomplete. Tests inject fakes; production uses protocolSearch.
type SearchFunc func(ctx context.Context, t Target, terms []FieldTerm, limit int) ([]*codex.Record, error)

// SearchAll fans the query out to every configured target (or the named
// subset) concurrently and returns the normalized hits; per-target failures
// come back as errors keyed by target name rather than failing the fan-out.
// A bare query searches the server-choice "any" index; fields AND onto it.
//
// warnings names the targets whose answer is incomplete but usable: a stream
// that broke after some records, or one the search limit cut short. Those
// targets' hits are in results -- a partial success is not a failure, and
// hiding the hits would throw away the useful half -- but a cataloger deciding
// "my book is not in this catalog" must be told the set is short.
func (s *Service) SearchAll(ctx context.Context, query string, fields []FieldTerm, names []string) ([]SearchResult, map[string]string, map[string]string, error) {
	terms, err := searchTerms(query, fields)
	if err != nil {
		return nil, nil, nil, err
	}
	targets, err := s.Targets(ctx)
	if err != nil {
		return nil, nil, nil, err
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
		return nil, nil, nil, fmt.Errorf("%w: no search targets configured", ErrValidation)
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
	warnings := map[string]string{}
	for _, t := range targets {
		wg.Add(1)
		go func(t Target) {
			defer wg.Done()
			recs, err := search(ctx, t, terms, searchLimit)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				// An incomplete answer is not a failed one: the records that
				// arrived are still the cataloger's answer, and dropping them
				// here is what made a mid-stream break invisible.
				if !Incomplete(err) {
					failures[t.Name] = err.Error()
					return
				}
				warnings[t.Name] = err.Error()
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
	return results, failures, warnings, nil
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
		return readUpTo(rd, limit)
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
	return readUpTo(rd, limit)
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

// unknownTotal is what a reader reports when the target never said how large the
// result set is: before the first fetch, and for the whole life of a stream
// whose server omits the count (SRU 2.0 permits that). It is not zero -- zero is
// the real answer "nothing matched" -- and collapsing the two would turn "we
// cannot tell you" into "there is nothing there" (libcodex).
const unknownTotal = -1

// readUpTo drains a record stream to limit, returning whatever arrived and, when
// the answer is incomplete, why.
//
// Partial results beat none: a mid-stream break after hits keeps the hits. But
// the records and the reason are both needed, and the old signature could carry
// only one, so the reason was dropped. Whether a given error lands
// on the first read or the fiftieth is decided by the remote server's page size
// -- an implementation detail of that server, not a property of the error -- so
// the same broken response was reported on page 1 and silently swallowed on
// page 2. Copy cataloging turns on "is my book in this result set?", and a
// truncated set answers that wrongly.
//
// Callers distinguish the four outcomes with errors.As / errors.Is:
//
//   - nil                  every record the target has is here.
//   - *PartialError        the stream broke; out holds what arrived first.
//   - ErrCapped            limit stopped us short of the result set.
//   - any other error      nothing arrived; the search failed.
func readUpTo(rd codex.RecordReader, limit int) ([]*codex.Record, error) {
	var out []*codex.Record
	for len(out) < limit {
		rec, err := rd.Read()
		if errors.Is(err, io.EOF) {
			return out, nil
		}
		if err != nil {
			if len(out) > 0 {
				return out, &PartialError{Got: len(out), Total: advertisedTotal(rd), Err: err}
			}
			return nil, err
		}
		out = append(out, rec)
	}
	return out, cappedError(advertisedTotal(rd), len(out), limit)
}

// advertisedTotal asks the reader how large the result set is. A reader over a
// file or a pipe has no result set to size, so it implements no counter and the
// answer is unknown (libcodex exposes this as codex.RecordCounter).
func advertisedTotal(rd codex.RecordReader) int {
	rc, ok := rd.(codex.RecordCounter)
	if !ok {
		return unknownTotal
	}
	return rc.Total()
}

// cappedError says how much of the result set the limit cut off, or reports a
// full page as complete.
//
// Reaching the limit is not the same as being truncated. Before libcodex v0.23.0
// exposed the advertised total, copycat could not tell "20 matches" from "the
// first 20 of 4,113", so it warned on every full page -- noise on the common
// case, and no help on the case that mattered. It can
// tell now, so a target holding exactly limit records is a complete answer and
// says nothing.
func cappedError(total, got, limit int) error {
	switch {
	case total > got:
		return fmt.Errorf("%w: showing %d of %d matches -- refine your search", ErrCapped, got, total)
	case total == got:
		return nil // the result set is exactly this size: nothing was cut off.
	default:
		// unknownTotal, or a server whose count contradicts its own stream. The
		// honest answer is the one we could always give.
		return fmt.Errorf("%w: showing the first %d", ErrCapped, limit)
	}
}
