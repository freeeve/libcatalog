package bibframe

import (
	"github.com/freeeve/libcodex/rdf"
)

// PredOverrides marks an editorial property ownership claim: the statement
// <subject> lcat:overrides <predicateIRI> in the editorial graph declares
// that, for this subject and predicate, every feed:* statement is shadowed --
// consumers (projector, doc mapper, MARC materializer) show only the
// editorial values. This gives replace (marker + editorial values), partial
// removal (marker + the kept subset re-asserted editorially), full removal
// (marker alone), and one-click revert (delete the marker and the editorial
// values; the feed resurfaces) -- all without reification, and surviving
// re-ingest because the editorial graph is preserved verbatim
// (ARCHITECTURE §5, tasks/042).
const PredOverrides = LcatNS + "overrides"

// OverridePatch builds the editorial patch claiming ownership of the given
// predicates on one subject node (apply with ApplyEditorialPatch; pair with
// the replacement values in the same patch).
func OverridePatch(subjectIRI string, predicates ...string) Patch {
	p := Patch{}
	for _, pred := range predicates {
		p.Add = append(p.Add, rdf.Quad{
			S: rdf.NewIRI(subjectIRI),
			P: rdf.NewIRI(PredOverrides),
			O: rdf.NewIRI(pred),
		})
	}
	return p
}

// RevertPatch builds the editorial patch releasing ownership (the removal
// half of revert; the caller also removes its editorial replacement values).
func RevertPatch(subjectIRI string, predicates ...string) Patch {
	p := OverridePatch(subjectIRI, predicates...)
	return Patch{Remove: p.Add}
}

// Overrides is the shadow set scanned from a dataset: subject IRI ->
// overridden predicate IRIs.
type Overrides map[string]map[string]bool

// Shadows reports whether (subject, predicate) is editorially owned.
func (o Overrides) Shadows(subject, predicate string) bool {
	return o[subject][predicate]
}

// ScanOverrides collects the editorial lcat:overrides markers from a parsed
// dataset.
func ScanOverrides(ds *rdf.Dataset) Overrides {
	ed := EditorialGraph()
	out := Overrides{}
	for _, q := range ds.Quads {
		if q.G != ed || q.P.Value != PredOverrides || !q.S.IsIRI() || !q.O.IsIRI() {
			continue
		}
		preds := out[q.S.Value]
		if preds == nil {
			preds = map[string]bool{}
			out[q.S.Value] = preds
		}
		preds[q.O.Value] = true
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// ApplyShadow returns g without the triples the overrides shadow -- the
// filter consumers run over feed-class graphs before reading them, so
// editorially-owned properties show only their editorial values.
func ApplyShadow(g *rdf.Graph, overrides Overrides) *rdf.Graph {
	if g == nil || len(overrides) == 0 {
		return g
	}
	filtered := &rdf.Graph{}
	filtered.Triples = make([]rdf.Triple, 0, len(g.Triples))
	for i := range g.Triples {
		tr := &g.Triples[i]
		if tr.S.IsIRI() && overrides.Shadows(tr.S.Value, tr.P.Value) {
			continue
		}
		filtered.Triples = append(filtered.Triples, *tr)
	}
	return filtered
}
