// Package catalogindex builds a read-only, periodically-rebuildable analytics
// view of the whole catalog over an in-memory gochickpeas graph. It answers
// cross-work questions the per-grain scans and the workindex identity layer
// cannot: which works share a controlled subject authority, and how heavily
// each authority is used across the corpus.
//
// The view is loaded from a catalog.nq dataset (the `lcat serialize` output),
// where each bf:Work is a "Work"-labelled node, each bf:subject link is an
// outgoing "subject" rel to the authority node, and every IRI node carries its
// own IRI under the "uri" property. Blob grains remain the source of truth;
// this Snapshot is derived and disposable -- rebuild a fresh one when the
// catalog changes rather than mutating an existing one.
//
// Subject authorities are the cross-work dimension modelled here because they
// are IRI-identified and therefore shared across works. Agents are blank nodes
// scoped to their grain (a MARC feed mints a fresh blank per contribution), so
// two works by the same author do not share an agent node; author co-occurrence
// stays a string-keyed concern of the similar package, not a graph query.
package catalogindex

import (
	"fmt"
	"sort"
	"sync"

	chickpeas "github.com/freeeve/gochickpeas"

	"github.com/freeeve/libcat/project"
)

// Snapshot is an immutable analytics view of one catalog dataset. Build it with
// Open or FromNQuads; its query methods are safe for concurrent use.
type Snapshot struct {
	g *chickpeas.Snapshot

	uriOnce  sync.Once
	uriIndex map[string]chickpeas.NodeID
}

// Open loads a catalog.nq dataset from disk into a Snapshot.
func Open(path string) (*Snapshot, error) {
	g, err := chickpeas.ReadNQuadsFile(path)
	if err != nil {
		return nil, fmt.Errorf("load catalog graph: %w", err)
	}
	return &Snapshot{g: g}, nil
}

// FromNQuads builds a Snapshot from an in-memory catalog.nq dataset.
func FromNQuads(data []byte) (*Snapshot, error) {
	g, err := chickpeas.ReadNQuads(data)
	if err != nil {
		return nil, fmt.Errorf("parse catalog graph: %w", err)
	}
	return &Snapshot{g: g}, nil
}

// AuthorityUse is one controlled subject authority and how many distinct works
// in the corpus are subject-linked to it.
type AuthorityUse struct {
	URI    string `json:"uri"`
	Label  string `json:"label,omitempty"`
	Scheme string `json:"scheme,omitempty"`
	Works  int    `json:"works"`
}

// AuthorityUsage reports every controlled subject authority referenced by at
// least one work, with the count of distinct works referencing it, sorted by
// descending use then URI. A subject a single work links more than once (e.g.
// asserted in both a feed graph and the editorial graph) counts once for that
// work.
//
// Only controlled authorities are reported -- subjects whose URI resolves to a
// recognized authority scheme (project.SchemeForURI). Bare-label topics (blank
// nodes with no URI) and per-grain local subjects (document-relative "#..."
// fragment IRIs) are omitted: they carry no shared cross-work identity to
// aggregate, which is the whole point of the tally.
func (s *Snapshot) AuthorityUsage() []AuthorityUse {
	works, ok := s.g.NodesWithLabel("Work")
	if !ok {
		return nil
	}
	counts := map[chickpeas.NodeID]int{}
	seen := map[chickpeas.NodeID]bool{}
	for w := range works.Iter() {
		clear(seen)
		for subj := range s.g.Neighbors(chickpeas.NodeID(w), chickpeas.Outgoing, "subject") {
			if seen[subj] {
				continue
			}
			seen[subj] = true
			counts[subj]++
		}
	}
	out := make([]AuthorityUse, 0, len(counts))
	for subj, n := range counts {
		uri := s.g.Prop(subj, "uri").StrOr("")
		scheme := project.SchemeForURI(uri)
		if scheme == "" {
			continue
		}
		out = append(out, AuthorityUse{
			URI:    uri,
			Label:  s.label(subj),
			Scheme: scheme,
			Works:  n,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Works != out[j].Works {
			return out[i].Works > out[j].Works
		}
		return out[i].URI < out[j].URI
	})
	return out
}

// WorksUsingAuthority returns the grain ids of the works subject-linked to the
// given authority URI, deduplicated and sorted. An unknown or unreferenced URI
// yields nil. The id is the local part of a work's "#<id>Work" node -- the same
// id the rest of libcat keys works by.
func (s *Snapshot) WorksUsingAuthority(uri string) []string {
	node, ok := s.subjectNode(uri)
	if !ok {
		return nil
	}
	ids := map[string]bool{}
	for w := range s.g.Neighbors(node, chickpeas.Incoming, "subject") {
		if id := workID(s.g.Prop(chickpeas.NodeID(w), "uri").StrOr("")); id != "" {
			ids[id] = true
		}
	}
	out := make([]string, 0, len(ids))
	for id := range ids {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// label returns a subject node's display label, preferring skos:prefLabel and
// falling back to rdfs:label -- the same order the projected catalog names a
// subject by.
func (s *Snapshot) label(node chickpeas.NodeID) string {
	if l := s.g.Prop(node, "prefLabel").StrOr(""); l != "" {
		return l
	}
	return s.g.Prop(node, "label").StrOr("")
}

// subjectNode resolves an IRI to the node that carries it, via a lazily-built
// URI index. The index is built once on first lookup so a Snapshot used only
// for AuthorityUsage never pays for it.
func (s *Snapshot) subjectNode(uri string) (chickpeas.NodeID, bool) {
	s.uriOnce.Do(func() {
		idx := make(map[string]chickpeas.NodeID)
		for n := uint32(0); n < s.g.NodeCount(); n++ {
			node := chickpeas.NodeID(n)
			if u := s.g.Prop(node, "uri").StrOr(""); u != "" {
				idx[u] = node
			}
		}
		s.uriIndex = idx
	})
	node, ok := s.uriIndex[uri]
	return node, ok
}

// workID maps a "#<id>Work" work-node IRI to its grain id, mirroring the
// projector's own fragment-id convention.
func workID(iri string) string {
	if len(iri) > 0 && iri[0] == '#' {
		iri = iri[1:]
	}
	const suffix = "Work"
	if n := len(iri) - len(suffix); n >= 0 && iri[n:] == suffix {
		return iri[:n]
	}
	return iri
}
