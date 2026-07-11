// Package editor is the record-editing service surface: the JSON patch
// shape the API accepts, its validation against the editorial predicate
// whitelist, and the conversion to bibframe editorial patches. The typed
// WorkDoc mapper and operation lists layer on top in later tasks; this is
// the quad-level floor they compile down to.
package editor

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/freeeve/libcodex/rdf"

	"github.com/freeeve/libcat/bibframe"
)

// Term is the JSON wire form of an RDF term.
type Term struct {
	// Kind is "iri" or "literal".
	Kind  string `json:"kind"`
	Value string `json:"value"`
	Lang  string `json:"lang,omitempty"`
	// Datatype is the literal datatype IRI, when not a plain/lang string.
	Datatype string `json:"datatype,omitempty"`
}

// Statement is the JSON wire form of one editorial statement. Subjects are
// IRIs (typically "#<id>Work" / "#<id>Instance" fragments); objects are IRIs
// or literals -- never blank nodes, per the editorial-graph constraint.
type Statement struct {
	S string `json:"s"`
	P string `json:"p"`
	O Term   `json:"o"`
}

// Patch is the request body of a record edit: statements to add to and
// remove from the editorial graph.
type Patch struct {
	Add    []Statement `json:"add,omitempty"`
	Remove []Statement `json:"remove,omitempty"`
}

// DefaultPredicateAllowlist is the editorial predicate policy when a
// deployment configures none: BIBFRAME descriptive predicates, libcat's
// lcat: extension markers, and SKOS term descriptions. A fully open
// editorial graph would let one bad request poison projector output.
var DefaultPredicateAllowlist = []string{
	"http://id.loc.gov/ontologies/bibframe/",
	"http://id.loc.gov/ontologies/bflc/",
	"https://github.com/freeeve/libcat/ns#",
	"http://www.w3.org/2004/02/skos/core#",
	"http://www.w3.org/2000/01/rdf-schema#label",
}

// Validate checks the patch's shape against the allowlist (nil = default).
func (p Patch) Validate(allowlist []string) error {
	if len(p.Add) == 0 && len(p.Remove) == 0 {
		return errors.New("editor: empty patch")
	}
	if len(p.Add)+len(p.Remove) > 500 {
		return errors.New("editor: patch too large (max 500 statements)")
	}
	if allowlist == nil {
		allowlist = DefaultPredicateAllowlist
	}
	for _, st := range append(append([]Statement{}, p.Add...), p.Remove...) {
		if st.S == "" || st.P == "" || st.O.Value == "" {
			return errors.New("editor: statement with empty term")
		}
		if st.O.Kind != "iri" && st.O.Kind != "literal" {
			return fmt.Errorf("editor: object kind %q (want iri or literal)", st.O.Kind)
		}
		if !allowed(st.P, allowlist) {
			return fmt.Errorf("editor: predicate %s not in the editorial allowlist", st.P)
		}
	}
	return nil
}

// Rebindable reports whether p can be applied to a work other than the one its
// subjects name -- the question a batch edit has to answer before it writes
// anything.
//
// A batch patch carries one literal subject. Applied verbatim to every selected
// work, it writes quads describing the first work into every other work's
// grain, and the dry run agrees with the corruption because it diffs the same
// verbatim patch. The subject has to be rebound per work, which is only
// meaningful for a Work node: an Instance id or a skolem child names a node in
// one grain and nothing at all in another. A grain-local object has the same
// problem. Both are refused rather than guessed at.
func (p Patch) Rebindable() error {
	for _, st := range append(append([]Statement{}, p.Add...), p.Remove...) {
		if _, ok := bibframe.WorkIDFromIRI(st.S); !ok {
			return fmt.Errorf("editor: subject %s is not a Work node; a batch patch edits each work's own Work node", st.S)
		}
		if st.O.Kind == "iri" && bibframe.GrainLocalIRI(st.O.Value) {
			return fmt.Errorf("editor: object %s names a node inside one grain; a batch patch cannot reference it", st.O.Value)
		}
	}
	return nil
}

// BoundTo checks that every Work-node subject in p names workID -- the
// single-record form of the rule. A patch aimed at one work has no
// business asserting statements about another work's node, and a grain that
// describes a Work it does not contain is invisible to every reader that
// resolves from the Work id inward.
//
// Subjects that are not Work nodes (an Instance node, a skolem child, an
// absolute IRI) pass through: those are the shapes a single-record editorial
// patch legitimately mints, and unlike the batch case there is nothing to
// rebind them to.
func (p Patch) BoundTo(workID string) error {
	for _, st := range append(append([]Statement{}, p.Add...), p.Remove...) {
		named, ok := bibframe.WorkIDFromIRI(st.S)
		if ok && named != workID {
			return fmt.Errorf("editor: subject %s names work %s, not %s", st.S, named, workID)
		}
	}
	return nil
}

// RebindWork returns p with every subject rebound to workID's Work node, so
// applying it to that work states something about that work. Call Rebindable
// first; a subject that is not a Work node is left alone here rather than
// silently rewritten.
func (p Patch) RebindWork(workID string) Patch {
	subject := bibframe.WorkIRI(workID)
	rebind := func(in []Statement) []Statement {
		if in == nil {
			return nil
		}
		out := make([]Statement, len(in))
		for i, st := range in {
			if _, ok := bibframe.WorkIDFromIRI(st.S); ok {
				st.S = subject
			}
			out[i] = st
		}
		return out
	}
	return Patch{Add: rebind(p.Add), Remove: rebind(p.Remove)}
}

func allowed(predicate string, allowlist []string) bool {
	for _, prefix := range allowlist {
		if strings.HasPrefix(predicate, prefix) {
			return true
		}
	}
	return false
}

// ToBibframe converts the wire patch to a bibframe editorial patch.
func (p Patch) ToBibframe() bibframe.Patch {
	return bibframe.Patch{Add: quads(p.Add), Remove: quads(p.Remove)}
}

func quads(statements []Statement) []rdf.Quad {
	out := make([]rdf.Quad, 0, len(statements))
	for _, st := range statements {
		q := rdf.Quad{S: rdf.NewIRI(st.S), P: rdf.NewIRI(st.P)}
		if st.O.Kind == "iri" {
			q.O = rdf.NewIRI(st.O.Value)
		} else {
			q.O = rdf.NewLiteral(st.O.Value, st.O.Lang, st.O.Datatype)
		}
		out = append(out, q)
	}
	return out
}

// Diff describes the exact grain change a patch would make (the dry-run /
// diff-preview payload): canonical N-Quads lines added and removed.
type Diff struct {
	Added   []string `json:"added"`
	Removed []string `json:"removed"`
}

// Empty reports that the patch changes nothing. A save whose diff is empty must
// not write, republish, or audit: the grain store is content-addressed so the
// write is a no-op anyway, but the audit trail would gain a RECORD_EDIT row
// naming an etag identical to its predecessor's, indistinguishable from a real
// edit.
func (d Diff) Empty() bool {
	return len(d.Added) == 0 && len(d.Removed) == 0
}

// ComputeDiff applies the patch to a copy of the grain and reports the
// line-level delta between the canonical forms.
func ComputeDiff(grain []byte, p Patch) (Diff, []byte, error) {
	updated, err := bibframe.ApplyEditorialPatch(grain, p.ToBibframe())
	if err != nil {
		return Diff{}, nil, err
	}
	before := lineSet(grain)
	after := lineSet(updated)
	diff := Diff{Added: []string{}, Removed: []string{}}
	for line := range after {
		if !before[line] {
			diff.Added = append(diff.Added, line)
		}
	}
	for line := range before {
		if !after[line] {
			diff.Removed = append(diff.Removed, line)
		}
	}
	sort.Strings(diff.Added)
	sort.Strings(diff.Removed)
	return diff, updated, nil
}

// DiffLines reports the canonical N-Quads lines added and removed between
// two grains -- the diff-preview payload for ops and patches alike.
func DiffLines(before, after []byte) Diff {
	b, a := lineSet(before), lineSet(after)
	diff := Diff{Added: []string{}, Removed: []string{}}
	for line := range a {
		if !b[line] {
			diff.Added = append(diff.Added, line)
		}
	}
	for line := range b {
		if !a[line] {
			diff.Removed = append(diff.Removed, line)
		}
	}
	sort.Strings(diff.Added)
	sort.Strings(diff.Removed)
	return diff
}

func lineSet(nq []byte) map[string]bool {
	set := map[string]bool{}
	for line := range strings.SplitSeq(string(nq), "\n") {
		if line != "" {
			set[line] = true
		}
	}
	return set
}
