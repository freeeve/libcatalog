package bibframe

import (
	"github.com/freeeve/libcodex/rdf"
)

// Work visibility (tasks/051): the delete stance is never row-deletion --
// a tombstoned Work disappears from projection and leaves a redirect entry
// (to a successor Work when named, else an empty target the host serves as
// gone), while a suppressed Work merely hides from projection with no
// redirect and is fully restorable. Both are editorial statements, so they
// survive re-ingest like every other curation decision.
const (
	// PredTombstoned marks a retired Work: object is the successor Work IRI
	// (redirect target) or the literal "true" (gone, no successor).
	PredTombstoned = LcatNS + "tombstoned"
	// PredSuppressed hides a Work from projection: object literal "true".
	PredSuppressed = LcatNS + "suppressed"
)

// WorkVisibility is one Work's projection stance.
type WorkVisibility struct {
	Tombstoned bool   `json:"tombstoned"`
	RedirectTo string `json:"redirectTo,omitempty"` // successor Work id, when named
	Suppressed bool   `json:"suppressed"`
}

// Visibility reads a Work's stance from its grain.
func Visibility(grainNQ []byte, workID string) (WorkVisibility, error) {
	ds, err := rdf.ParseNQuads(grainNQ)
	if err != nil {
		return WorkVisibility{}, err
	}
	ed := EditorialGraph()
	work := WorkIRI(workID)
	var v WorkVisibility
	for _, q := range ds.Quads {
		if q.G != ed || !q.S.IsIRI() || q.S.Value != work {
			continue
		}
		switch q.P.Value {
		case PredTombstoned:
			v.Tombstoned = true
			if q.O.IsIRI() {
				v.RedirectTo = fragWork(q.O.Value)
			}
		case PredSuppressed:
			v.Suppressed = q.O.Value == "true"
		}
	}
	return v, nil
}

// SetTombstone retires a Work: any prior tombstone statement is replaced by
// one pointing at redirectTo (a Work id) or, with redirectTo empty, the
// no-successor literal. Returns the re-canonicalized grain.
func SetTombstone(grainNQ []byte, workID, redirectTo string) ([]byte, error) {
	object := rdf.NewLiteral("true", "", "")
	if redirectTo != "" {
		object = rdf.NewIRI(WorkIRI(redirectTo))
	}
	return replaceWorkStatement(grainNQ, workID, PredTombstoned, &object)
}

// ClearTombstone restores a retired Work.
func ClearTombstone(grainNQ []byte, workID string) ([]byte, error) {
	return replaceWorkStatement(grainNQ, workID, PredTombstoned, nil)
}

// SetSuppressed hides or unhides a Work from projection.
func SetSuppressed(grainNQ []byte, workID string, suppressed bool) ([]byte, error) {
	if !suppressed {
		return replaceWorkStatement(grainNQ, workID, PredSuppressed, nil)
	}
	object := rdf.NewLiteral("true", "", "")
	return replaceWorkStatement(grainNQ, workID, PredSuppressed, &object)
}

// replaceWorkStatement drops every editorial (work, pred, *) statement and,
// when object is non-nil, asserts the replacement -- the set/clear primitive
// visibility shares.
func replaceWorkStatement(grainNQ []byte, workID, pred string, object *rdf.Term) ([]byte, error) {
	ds, err := rdf.ParseNQuads(grainNQ)
	if err != nil {
		return nil, err
	}
	ed := EditorialGraph()
	work := WorkIRI(workID)
	keep := ds.Quads[:0]
	for _, q := range ds.Quads {
		if q.G == ed && q.S.IsIRI() && q.S.Value == work && q.P.Value == pred {
			continue
		}
		keep = append(keep, q)
	}
	ds.Quads = keep
	stripped, err := ds.Canonical()
	if err != nil {
		return nil, err
	}
	if object == nil {
		return stripped, nil
	}
	return ApplyEditorialPatch(stripped, Patch{Add: []rdf.Quad{{
		S: rdf.NewIRI(work), P: rdf.NewIRI(pred), O: *object,
	}}})
}
