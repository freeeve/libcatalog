package bibframe

import (
	"fmt"
	"sort"
	"strings"

	"github.com/freeeve/libcodex/rdf"
)

// Work-to-work relationship predicates the editor writes (058
// item 3): the BF 2.0 whole/part inverses. A link is stored in both works'
// editorial graphs -- hasPart on the whole, partOf on the part -- so each
// grain self-describes and neither side needs a corpus scan to render its
// panel. Targets use the same #<id>Work fragment convention the merge and
// tombstone markers use for cross-grain work references.
const (
	PredHasPart = "http://id.loc.gov/ontologies/bibframe/hasPart"
	PredPartOf  = "http://id.loc.gov/ontologies/bibframe/partOf"
)

// WorkRelations lists one work's editorial work-to-work links as work ids.
type WorkRelations struct {
	HasPart []string `json:"hasPart"`
	PartOf  []string `json:"partOf"`
}

// WorkRelationsOf reads a grain's editorial hasPart/partOf links for the
// given work, sorted by target id.
func WorkRelationsOf(grainNQ []byte, workID string) (WorkRelations, error) {
	ds, err := rdf.ParseNQuads(grainNQ)
	if err != nil {
		return WorkRelations{}, err
	}
	work, editorial := WorkIRI(workID), EditorialGraph()
	var out WorkRelations
	for i := range ds.Quads {
		q := &ds.Quads[i]
		if q.G != editorial || !q.S.IsIRI() || q.S.Value != work {
			continue
		}
		target := workIDFromIRI(q.O)
		if target == "" {
			continue
		}
		switch q.P.Value {
		case PredHasPart:
			out.HasPart = append(out.HasPart, target)
		case PredPartOf:
			out.PartOf = append(out.PartOf, target)
		}
	}
	sort.Strings(out.HasPart)
	sort.Strings(out.PartOf)
	return out, nil
}

// SetWorkRelation adds or removes one editorial relation statement on the
// grain, guarded on the grain describing the work (the 202/211/214 family:
// a typo'd id must not assert links into a foreign grain). An add is also
// refused when the grain already asserts the inverse predicate to the same
// target, so no caller can write the contradiction "A contains B and A is a
// part of B"; the handler catches the same pair (and longer
// cycles) first, this is the backstop that keeps it out of any grain.
// Adds are idempotent through canonicalization.
func SetWorkRelation(grainNQ []byte, workID, pred, targetID string, add bool) ([]byte, error) {
	if pred != PredHasPart && pred != PredPartOf {
		return nil, fmt.Errorf("unknown relation predicate %q", pred)
	}
	if !grainDescribesWork(grainNQ, workID) {
		return nil, fmt.Errorf("grain does not describe work %s", workID)
	}
	if add {
		held, err := WorkRelationsOf(grainNQ, workID)
		if err != nil {
			return nil, err
		}
		opposite := held.PartOf
		if pred == PredPartOf {
			opposite = held.HasPart
		}
		for _, t := range opposite {
			if t == targetID {
				return nil, fmt.Errorf("work %s already asserts the inverse relation to %s", workID, targetID)
			}
		}
	}
	q := rdf.Quad{S: rdf.NewIRI(WorkIRI(workID)), P: rdf.NewIRI(pred), O: rdf.NewIRI(WorkIRI(targetID))}
	patch := Patch{Add: []rdf.Quad{q}}
	if !add {
		patch = Patch{Remove: []rdf.Quad{q}}
	}
	return ApplyEditorialPatch(grainNQ, patch)
}

// workIDFromIRI recovers the work id from a #<id>Work fragment term; ""
// for anything else.
func workIDFromIRI(t rdf.Term) string {
	if !t.IsIRI() || !strings.HasPrefix(t.Value, "#") || !strings.HasSuffix(t.Value, "Work") {
		return ""
	}
	return t.Value[1 : len(t.Value)-len("Work")]
}

// grainDescribesWork reports whether any statement's subject is the work's
// node.
func grainDescribesWork(grainNQ []byte, workID string) bool {
	ds, err := rdf.ParseNQuads(grainNQ)
	if err != nil {
		return false
	}
	work := WorkIRI(workID)
	for i := range ds.Quads {
		if q := &ds.Quads[i]; q.S.IsIRI() && q.S.Value == work {
			return true
		}
	}
	return false
}
