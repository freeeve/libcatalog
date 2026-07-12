package bibframe

import (
	"fmt"

	"github.com/freeeve/libcodex/rdf"
)

// SplitCrossGraphBlanks repairs fused-blank corruption in one grain: a blank
// node whose label appears in more than one named graph was almost certainly
// two unrelated nodes fused by the pre-fix write path (per-graph blank
// numbering colliding in one file), so each such node is split back into
// per-graph copies and the grain re-canonicalized. Returns the repaired bytes
// and how many fused blanks were split; zero means the grain is clean and the
// original bytes come back untouched.
//
// Splitting is safe for grains libcat wrote: no writer intentionally states
// one blank across graphs. A legitimately shared node would duplicate -- each
// graph keeps its own copy of the statements it made, which is exactly what
// the statements said per graph.
func SplitCrossGraphBlanks(grain []byte) ([]byte, int, error) {
	ds, err := rdf.ParseNQuads(grain)
	if err != nil {
		return nil, 0, fmt.Errorf("parse grain: %w", err)
	}
	graphsOf := map[string]map[string]bool{}
	for _, q := range ds.Quads {
		for _, term := range []rdf.Term{q.S, q.O} {
			if !term.IsBlank() {
				continue
			}
			set := graphsOf[term.Value]
			if set == nil {
				set = map[string]bool{}
				graphsOf[term.Value] = set
			}
			set[q.G.Value] = true
		}
	}
	fused := map[string]bool{}
	for label, graphs := range graphsOf {
		if len(graphs) > 1 {
			fused[label] = true
		}
	}
	if len(fused) == 0 {
		return grain, 0, nil
	}

	// Rename each fused blank per graph: the (label, graph) pair becomes its
	// own node. Graph indexes are first-seen order, stable within this parse;
	// the joint canonicalization below erases the synthetic labels anyway.
	graphIdx := map[string]int{}
	idxOf := func(g rdf.Term) int {
		if i, ok := graphIdx[g.Value]; ok {
			return i
		}
		graphIdx[g.Value] = len(graphIdx)
		return graphIdx[g.Value]
	}
	split := func(term rdf.Term, g rdf.Term) rdf.Term {
		if !term.IsBlank() || !fused[term.Value] {
			return term
		}
		return rdf.NewBlank(fmt.Sprintf("%s_split%d", term.Value, idxOf(g)))
	}
	for i := range ds.Quads {
		q := &ds.Quads[i]
		q.S = split(q.S, q.G)
		q.O = split(q.O, q.G)
	}
	out, err := ds.Canonical()
	if err != nil {
		return nil, 0, fmt.Errorf("canonicalize: %w", err)
	}
	return out, len(fused), nil
}
