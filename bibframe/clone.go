package bibframe

import (
	"fmt"
	"strings"

	"github.com/freeeve/libcodex/rdf"

	"github.com/freeeve/libcat/identity"
)

// cloneDropsSubgraph reports whether a statement seeds a subgraph the clone
// must not carry: provider identifiers (the 058 acceptance -- a clone's
// provider keys are gone, and keeping them would trip identifier-based
// duplicate resolution), the source record's admin metadata (the clone is a
// fresh editorial description, so its 040 derives from the clone's own graph
// facts per tasks/192), holdings (the physical copies belong to the source
// work), and uncontrolled provider headings -- the blank-node bf:subject /
// bf:genreForm shapes. Blank headings must not skolemize (an IRI object of
// bf:subject reads as a controlled term everywhere) and blank editorial
// statements are unpatchable, so like identifiers they stay with the source;
// controlled subject IRIs carry over.
func cloneDropsSubgraph(q *rdf.Quad) bool {
	switch q.P.Value {
	case "http://id.loc.gov/ontologies/bibframe/identifiedBy",
		"http://id.loc.gov/ontologies/bibframe/adminMetadata",
		predHasItem:
		return true
	case bfSubjectIRI, "http://id.loc.gov/ontologies/bibframe/genreForm":
		return q.O.IsBlank()
	}
	return false
}

// CloneGrain builds a brand-new work grain from a source grain's
// description (tasks/217, 058 item 4): the work and every instance are
// re-minted, every kept statement lands in the editorial graph (the clone
// has no feed -- it is hand-authored from here on), identifier / admin-
// metadata / holdings subgraphs are dropped, lcat curation markers (tags,
// visibility, merge/split pins, extras) stay with the source, and the
// clone is born suppressed so it hides from projection until a cataloger
// finishes and publishes it. Returns the canonical grain and the fresh
// work id.
func CloneGrain(src []byte, workID string) ([]byte, string, error) {
	ds, err := rdf.ParseNQuads(src)
	if err != nil {
		return nil, "", err
	}
	oldWork := WorkIRI(workID)
	described := false
	for i := range ds.Quads {
		if q := &ds.Quads[i]; q.S.IsIRI() && q.S.Value == oldWork {
			described = true
			break
		}
	}
	if !described {
		return nil, "", fmt.Errorf("grain does not describe work %s", workID)
	}

	newID := identity.Mint(identity.WorkPrefix)
	rename := map[string]string{oldWork: WorkIRI(newID)}
	drop := map[string]bool{}
	for i := range ds.Quads {
		q := &ds.Quads[i]
		if cloneDropsSubgraph(q) {
			drop[q.O.Value] = true
		}
		if q.S.IsIRI() && strings.HasSuffix(q.S.Value, "Instance") && strings.HasPrefix(q.S.Value, "#") {
			if _, ok := rename[q.S.Value]; !ok {
				rename[q.S.Value] = InstanceIRI(identity.Mint(identity.InstancePrefix))
			}
		}
	}
	// Close the drop set over blank-node children (identifier type/value
	// nodes, item fields): shared named nodes like an org IRI stay.
	for grew := true; grew; {
		grew = false
		for i := range ds.Quads {
			q := &ds.Quads[i]
			if drop[q.S.Value] && q.O.IsBlank() && !drop[q.O.Value] {
				drop[q.O.Value] = true
				grew = true
			}
		}
	}

	// Skolemize blank nodes to grain-local fragment IRIs: every clone
	// statement is editorial, and the editorial patch machinery refuses
	// blank-node patches (their identity is canonicalization-label-
	// dependent), so a blank title/contribution node would make those
	// fields uneditable -- the opposite of what a clone is for. The source
	// is canonical, so first-appearance order names them deterministically.
	skolem := 0
	editorial := EditorialGraph()
	out := &rdf.Dataset{}
	for i := range ds.Quads {
		q := &ds.Quads[i]
		if cloneDropsSubgraph(q) || drop[q.S.Value] || drop[q.O.Value] || strings.HasPrefix(q.P.Value, LcatNS) {
			continue
		}
		for _, t := range []rdf.Term{q.S, q.O} {
			if t.IsBlank() {
				if _, ok := rename[t.Value]; !ok {
					skolem++
					rename[t.Value] = fmt.Sprintf("#%sn%d", newID, skolem)
				}
			}
		}
		out.Add(renameTerm(q.S, rename), q.P, renameTerm(q.O, rename), editorial)
	}
	nq, err := out.Canonical()
	if err != nil {
		return nil, "", err
	}
	nq, err = SetSuppressed(nq, newID, true)
	if err != nil {
		return nil, "", err
	}
	return nq, newID, nil
}

// GrainLocalIRI reports whether an IRI value is grain-local -- a fragment
// node the grain itself minted (#<id>Work, #<id>Instance, an editor or
// clone skolem) rather than a reference into an external vocabulary. The
// subjects model keys on this (tasks/218): a bf:subject object that is a
// grain-local node is an uncontrolled heading whose rdfs:label is the
// value, never a controlled term, exactly like the blank nodes it stands
// in for.
func GrainLocalIRI(v string) bool { return strings.HasPrefix(v, "#") }

// renameTerm maps a grain-local node (the Work and Instance fragment IRIs,
// a skolemized blank node) onto the clone's freshly minted identity; every
// other term passes through untouched.
func renameTerm(t rdf.Term, rename map[string]string) rdf.Term {
	if t.IsIRI() || t.IsBlank() {
		if to, ok := rename[t.Value]; ok {
			return rdf.NewIRI(to)
		}
	}
	return t
}
