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
// facts), and holdings (the physical copies belong to the
// source work).
//
// Subject and genre headings are NOT dropped, controlled or not.
// They were, on the reasoning that a skolemized blank heading "reads as a
// controlled term everywhere" -- which made false the same week, by
// teaching both readers (project.subjectsAndTags, ingest.SummarizeDataset)
// that a grain-local IRI object of bf:subject is an uncontrolled heading, the
// same as the blank node it stands in for. See GrainLocalIRI. Uncontrolled
// headings are most of the subject access in a MARC-derived record, and
// dropping them was invisible to the cataloger and visible to the reader as a
// missing facet.
func cloneDropsSubgraph(q *rdf.Quad) bool {
	switch q.P.Value {
	case "http://id.loc.gov/ontologies/bibframe/identifiedBy",
		"http://id.loc.gov/ontologies/bibframe/adminMetadata",
		PredHasItem:
		return true
	case PredHasPart, PredPartOf:
		// Work-to-work links are stored in both grains; a clone
		// carrying its source's side would be a half-link no other grain
		// reciprocates.
		return true
	}
	return false
}

// CloneGrain builds a brand-new work grain from a source grain's
// description: the work and every instance are
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
		// An instance can be named from either end (bf:hasInstance names it as
		// an object before its rdf:type names it as a subject).
		for _, t := range []rdf.Term{q.S, q.O} {
			if cloneInstanceIRI(t) {
				if _, ok := rename[t.Value]; !ok {
					rename[t.Value] = InstanceIRI(identity.Mint(identity.InstancePrefix))
				}
			}
		}
	}
	// Close the drop set over the dropped nodes' children -- blank ones
	// (identifier type/value nodes, item fields) and grain-local ones, which
	// is what those children are in a grain that has already been cloned
	// once. Shared named nodes like an org IRI stay.
	for grew := true; grew; {
		grew = false
		for i := range ds.Quads {
			q := &ds.Quads[i]
			if !drop[q.S.Value] || drop[q.O.Value] {
				continue
			}
			if q.O.IsBlank() || (q.O.IsIRI() && GrainLocalIRI(q.O.Value)) {
				drop[q.O.Value] = true
				grew = true
			}
		}
	}

	// Give every node the clone owns a fresh grain-local name.
	//
	// Blank nodes skolemize because every clone statement is editorial and the
	// editorial patch machinery refuses blank-node patches (their identity is
	// canonicalization-label-dependent), so a blank title node would make that
	// field uneditable -- the opposite of what a clone is for.
	//
	// Grain-local IRIs re-mint because GrainLocalIRI means "a node THIS grain
	// minted". A source that was itself cloned already carries #<sourceID>n<k>
	// nodes; passing them through would leave most of the new grain -- its
	// title node included -- named after an ancestor work, forever.
	// The Work and the instances are already in rename, so they keep their
	// dedicated identities.
	//
	// The source is canonical, so first-appearance order names both kinds
	// deterministically.
	skolem := 0
	editorial := EditorialGraph()
	out := &rdf.Dataset{}
	for i := range ds.Quads {
		q := &ds.Quads[i]
		if cloneDropsSubgraph(q) || drop[q.S.Value] || drop[q.O.Value] || strings.HasPrefix(q.P.Value, LcatNS) {
			continue
		}
		for _, t := range []rdf.Term{q.S, q.O} {
			if !t.IsBlank() && !(t.IsIRI() && GrainLocalIRI(t.Value)) {
				continue
			}
			if _, ok := rename[t.Value]; !ok {
				skolem++
				rename[t.Value] = fmt.Sprintf("#%sn%d", newID, skolem)
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
// subjects model keys on this: a bf:subject object that is a
// grain-local node is an uncontrolled heading whose rdfs:label is the
// value, never a controlled term, exactly like the blank nodes it stands
// in for.
func GrainLocalIRI(v string) bool { return strings.HasPrefix(v, "#") }

// cloneInstanceIRI reports whether a term names a grain-local Instance node.
func cloneInstanceIRI(t rdf.Term) bool {
	return t.IsIRI() && strings.HasPrefix(t.Value, "#") && strings.HasSuffix(t.Value, "Instance")
}

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
