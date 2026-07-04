// Package vocab loads controlled vocabularies from SKOS authority grains and
// serves the in-memory term index behind term validation, the picker's
// autocomplete, and neighborhood browsing. A vocabulary's quads live in its
// authority:<vocab> named graph (ARCHITECTURE §5), so the loader routes terms
// to schemes by graph name -- one authorities tree can carry homosaurus, lcsh,
// and local terms side by side. This replaces qllpoc's embedded
// homosaurus-min.json with a vocabulary-agnostic store-backed load.
package vocab

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync/atomic"

	"github.com/freeeve/libcodex/rdf"

	"github.com/freeeve/libcatalog/bibframe"
	"github.com/freeeve/libcatalog/storage/blob"
)

// SKOS predicate IRIs.
const (
	skosPrefLabel  = "http://www.w3.org/2004/02/skos/core#prefLabel"
	skosAltLabel   = "http://www.w3.org/2004/02/skos/core#altLabel"
	skosDefinition = "http://www.w3.org/2004/02/skos/core#definition"
	skosBroader    = "http://www.w3.org/2004/02/skos/core#broader"
	skosNarrower   = "http://www.w3.org/2004/02/skos/core#narrower"
	skosRelated    = "http://www.w3.org/2004/02/skos/core#related"
	skosExactMatch = "http://www.w3.org/2004/02/skos/core#exactMatch"
	skosCloseMatch = "http://www.w3.org/2004/02/skos/core#closeMatch"
	rdfsLabel      = "http://www.w3.org/2000/01/rdf-schema#label"
	// authorityGraphPrefix matches bibframe.AuthorityGraph's naming.
	authorityGraphPrefix = "authority:"
)

// Term is one controlled-vocabulary concept.
type Term struct {
	Scheme     string              `json:"scheme"`
	ID         string              `json:"id"`                   // the authority URI
	Labels     map[string]string   `json:"labels"`               // lang -> prefLabel ("" key = untagged)
	AltLabels  map[string][]string `json:"altLabels,omitempty"`  // lang -> used-for labels
	Definition map[string]string   `json:"definition,omitempty"` // lang -> scope note
	Broader    []string            `json:"broader,omitempty"`
	Narrower   []string            `json:"narrower,omitempty"`
	Related    []string            `json:"related,omitempty"`
	ExactMatch []string            `json:"exactMatch,omitempty"`
	CloseMatch []string            `json:"closeMatch,omitempty"`
	// MergedInto marks a retired term: it was merged into the referenced
	// URI (lcat:mergedInto, tasks/046). Retired terms resolve via Lookup
	// (so old references still label) but leave the search index.
	MergedInto string `json:"mergedInto,omitempty"`
}

// Label returns the term's best label for lang: exact match, then English,
// then untagged, then any.
func (t *Term) Label(lang string) string {
	for _, k := range []string{lang, "en", ""} {
		if l, ok := t.Labels[k]; ok {
			return l
		}
	}
	for _, l := range t.Labels {
		return l
	}
	return t.ID
}

// Index is the loaded term index. Reads are lock-free over an immutable
// snapshot; Reload builds a fresh snapshot and swaps it atomically, so every
// holder of the *Index (terms handler, suggestion gate, publisher) sees
// authority edits without rewiring (tasks/046).
type Index struct {
	snap atomic.Pointer[snapshot]
}

// snapshot is one immutable build of the index.
type snapshot struct {
	schemes map[string]map[string]*Term
	// search holds, per scheme, entries sorted by normalized label for
	// prefix search across pref and alt labels in every language.
	search map[string][]searchEntry
}

type searchEntry struct {
	norm string
	uri  string
	alt  bool
}

var emptySnapshot = &snapshot{}

// load returns the current snapshot, never nil.
func (ix *Index) load() *snapshot {
	if s := ix.snap.Load(); s != nil {
		return s
	}
	return emptySnapshot
}

// Load reads every authority grain under prefix from the store and indexes
// the terms of the requested schemes (nil/empty schemes = all authority
// graphs found).
func Load(ctx context.Context, st blob.Store, prefix string, schemes []string) (*Index, error) {
	ix := &Index{}
	if err := ix.Reload(ctx, st, prefix, schemes); err != nil {
		return nil, err
	}
	return ix, nil
}

// Reload rebuilds the index from the store and atomically swaps it in --
// the post-authority-edit refresh path. A failed reload leaves the previous
// snapshot serving.
func (ix *Index) Reload(ctx context.Context, st blob.Store, prefix string, schemes []string) error {
	s, err := buildSnapshot(ctx, st, prefix, schemes)
	if err != nil {
		return err
	}
	ix.snap.Store(s)
	return nil
}

func buildSnapshot(ctx context.Context, st blob.Store, prefix string, schemes []string) (*snapshot, error) {
	want := map[string]bool{}
	for _, s := range schemes {
		want[s] = true
	}
	snap := &snapshot{schemes: map[string]map[string]*Term{}, search: map[string][]searchEntry{}}
	for entry, err := range st.List(ctx, prefix) {
		if err != nil {
			return nil, fmt.Errorf("vocab: list authorities: %w", err)
		}
		if !strings.HasSuffix(entry.Path, ".nq") {
			continue
		}
		data, _, err := st.Get(ctx, entry.Path)
		if err != nil {
			return nil, fmt.Errorf("vocab: read %s: %w", entry.Path, err)
		}
		ds, err := rdf.ParseNQuads(data)
		if err != nil {
			return nil, fmt.Errorf("vocab: parse %s: %w", entry.Path, err)
		}
		snap.addDataset(ds, want)
	}
	snap.finish()
	return snap, nil
}

func (s *snapshot) addDataset(ds *rdf.Dataset, want map[string]bool) {
	for _, q := range ds.Quads {
		scheme, ok := strings.CutPrefix(q.G.Value, authorityGraphPrefix)
		if !ok || !q.S.IsIRI() {
			continue
		}
		if len(want) > 0 && !want[scheme] {
			continue
		}
		t := s.term(scheme, q.S.Value)
		switch q.P.Value {
		case skosPrefLabel:
			if q.O.IsLiteral() {
				t.Labels[q.O.Lang] = q.O.Value
			}
		case rdfsLabel:
			if q.O.IsLiteral() {
				if _, ok := t.Labels[q.O.Lang]; !ok {
					t.Labels[q.O.Lang] = q.O.Value
				}
			}
		case skosAltLabel:
			if q.O.IsLiteral() {
				if t.AltLabels == nil {
					t.AltLabels = map[string][]string{}
				}
				t.AltLabels[q.O.Lang] = append(t.AltLabels[q.O.Lang], q.O.Value)
			}
		case skosDefinition:
			if q.O.IsLiteral() {
				if _, ok := t.Definition[q.O.Lang]; !ok {
					if t.Definition == nil {
						t.Definition = map[string]string{}
					}
					t.Definition[q.O.Lang] = q.O.Value
				}
			}
		case skosBroader:
			if q.O.IsIRI() {
				t.Broader = appendUnique(t.Broader, q.O.Value)
			}
		case skosNarrower:
			if q.O.IsIRI() {
				t.Narrower = appendUnique(t.Narrower, q.O.Value)
			}
		case skosRelated:
			if q.O.IsIRI() {
				t.Related = appendUnique(t.Related, q.O.Value)
			}
		case skosExactMatch:
			if q.O.IsIRI() {
				t.ExactMatch = appendUnique(t.ExactMatch, q.O.Value)
			}
		case skosCloseMatch:
			if q.O.IsIRI() {
				t.CloseMatch = appendUnique(t.CloseMatch, q.O.Value)
			}
		case bibframe.PredMergedInto:
			if q.O.IsIRI() {
				t.MergedInto = q.O.Value
			}
		}
	}
}

func (s *snapshot) term(scheme, uri string) *Term {
	byURI := s.schemes[scheme]
	if byURI == nil {
		byURI = map[string]*Term{}
		s.schemes[scheme] = byURI
	}
	t := byURI[uri]
	if t == nil {
		t = &Term{Scheme: scheme, ID: uri, Labels: map[string]string{}}
		byURI[uri] = t
	}
	return t
}

// finish sorts relation lists and builds the per-scheme search slices.
// Retired (merged) terms stay resolvable but get no search entries.
func (s *snapshot) finish() {
	for scheme, byURI := range s.schemes {
		var entries []searchEntry
		for uri, t := range byURI {
			sort.Strings(t.Broader)
			sort.Strings(t.Narrower)
			sort.Strings(t.Related)
			sort.Strings(t.ExactMatch)
			sort.Strings(t.CloseMatch)
			if t.MergedInto != "" {
				continue
			}
			seen := map[string]bool{}
			for _, l := range t.Labels {
				if n := normLabel(l); n != "" && !seen[n] {
					seen[n] = true
					entries = append(entries, searchEntry{norm: n, uri: uri})
				}
			}
			for _, alts := range t.AltLabels {
				for _, l := range alts {
					if n := normLabel(l); n != "" && !seen[n] {
						seen[n] = true
						entries = append(entries, searchEntry{norm: n, uri: uri, alt: true})
					}
				}
			}
		}
		sort.Slice(entries, func(i, j int) bool {
			if entries[i].norm != entries[j].norm {
				return entries[i].norm < entries[j].norm
			}
			return entries[i].uri < entries[j].uri
		})
		s.search[scheme] = entries
	}
}

// Schemes lists the loaded vocabulary keys, sorted.
func (ix *Index) Schemes() []string {
	snap := ix.load()
	out := make([]string, 0, len(snap.schemes))
	for s := range snap.schemes {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// Lookup returns the term by scheme and URI -- the validation gate: only
// terms that resolve here are accepted into suggestions or subject edits.
func (ix *Index) Lookup(scheme, id string) (*Term, bool) {
	t, ok := ix.load().schemes[scheme][id]
	return t, ok
}

// Resolve returns the term for a URI regardless of scheme (schemes checked
// in sorted order for determinism) -- the editor's chip renderer resolves
// stored subject references without knowing where they came from (tasks/071).
func (ix *Index) Resolve(id string) (*Term, bool) {
	snap := ix.load()
	schemes := make([]string, 0, len(snap.schemes))
	for s := range snap.schemes {
		schemes = append(schemes, s)
	}
	sort.Strings(schemes)
	for _, s := range schemes {
		if t, ok := snap.schemes[s][id]; ok {
			return t, true
		}
	}
	return nil, false
}

// Terms returns every term of a scheme ordered by label -- the authorities
// management listing. Retired terms are included (marked by MergedInto).
func (ix *Index) Terms(scheme string) []*Term {
	byURI := ix.load().schemes[scheme]
	out := make([]*Term, 0, len(byURI))
	for _, t := range byURI {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool {
		li, lj := normLabel(out[i].Label("en")), normLabel(out[j].Label("en"))
		if li != lj {
			return li < lj
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// Search returns up to limit terms whose pref or alt label (any language)
// starts with q, deduped, ordered by label.
func (ix *Index) Search(scheme, q string, limit int) []*Term {
	snap := ix.load()
	entries := snap.search[scheme]
	norm := normLabel(q)
	if norm == "" || limit <= 0 {
		return nil
	}
	start := sort.Search(len(entries), func(i int) bool { return entries[i].norm >= norm })
	var out []*Term
	seen := map[string]bool{}
	for i := start; i < len(entries) && strings.HasPrefix(entries[i].norm, norm); i++ {
		uri := entries[i].uri
		if seen[uri] {
			continue
		}
		seen[uri] = true
		out = append(out, snap.schemes[scheme][uri])
		if len(out) >= limit {
			break
		}
	}
	return out
}

// Path returns the term's ancestor chain as TermRefs ordered root → … →
// direct parent (the term itself is excluded), following skos:broader.
// A polyhierarchical term takes the shortest chain to a root, ties broken
// by URI order; cycles and broader URIs missing from the scheme terminate
// the walk. A root term (or unknown term) yields nil.
func (ix *Index) Path(scheme, id string) []TermRef {
	byURI := ix.load().schemes[scheme]
	if byURI == nil || byURI[id] == nil {
		return nil
	}
	// BFS upward over broader edges: the first dequeued node with no
	// resolvable parent lies on a shortest chain. Broader lists are sorted
	// at load, so equal-length chains resolve to the smallest URIs.
	prev := map[string]string{id: ""}
	queue := []string{id}
	root := ""
	for len(queue) > 0 && root == "" {
		cur := queue[0]
		queue = queue[1:]
		parents := 0
		for _, b := range byURI[cur].Broader {
			if byURI[b] == nil {
				continue
			}
			parents++
			if _, seen := prev[b]; !seen {
				prev[b] = cur
				queue = append(queue, b)
			}
		}
		if parents == 0 {
			root = cur
		}
	}
	if root == "" || root == id {
		return nil
	}
	var path []TermRef
	for cur := root; cur != id; cur = prev[cur] {
		t := byURI[cur]
		path = append(path, TermRef{Scheme: scheme, ID: cur, Label: t.Label("en")})
	}
	return path
}

// LabelMatch is one exact-label hit: the term plus whether the match came
// through an alt (used-for) label rather than a preferred label.
type LabelMatch struct {
	Term *Term
	Alt  bool
}

// MatchLabel returns the scheme's terms whose pref or alt label normalizes
// exactly to label -- the auto-linking gate (tasks/046): only whole-heading
// matches produce suggestions, never prefix guesses.
func (ix *Index) MatchLabel(scheme, label string) []LabelMatch {
	snap := ix.load()
	entries := snap.search[scheme]
	norm := normLabel(label)
	if norm == "" {
		return nil
	}
	start := sort.Search(len(entries), func(i int) bool { return entries[i].norm >= norm })
	var out []LabelMatch
	seen := map[string]bool{}
	for i := start; i < len(entries) && entries[i].norm == norm; i++ {
		if seen[entries[i].uri] {
			continue
		}
		seen[entries[i].uri] = true
		out = append(out, LabelMatch{Term: snap.schemes[scheme][entries[i].uri], Alt: entries[i].alt})
	}
	return out
}

func normLabel(s string) string {
	return strings.Join(strings.Fields(strings.ToLower(s)), " ")
}

func appendUnique(list []string, v string) []string {
	if slices.Contains(list, v) {
		return list
	}
	return append(list, v)
}
