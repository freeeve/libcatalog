package bibframe

import (
	"github.com/freeeve/libcodex/rdf"
)

// Work visibility: the delete stance is never row-deletion --
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
	// PredWithdrawn flags a Work whose sole bib feed no longer lists it
	// ( reconciliation): object literal is the ISO date of the pass
	// that noticed. A flag, not a deletion -- identity and editorial
	// statements survive a title returning to the collection.
	PredWithdrawn = LcatNS + "withdrawnFromFeed"
	// PredSuppressedBy records what set the suppression ("feed-reconcile"
	// for the auto-suppress policy), so clearing a withdrawal un-suppresses
	// only suppressions that pass created.
	PredSuppressedBy = LcatNS + "suppressedBy"
	// PredFeedKept records a curator's "keep despite withdrawal" decision:
	// object literal "true". Reconciliation never re-flags a kept Work.
	PredFeedKept = LcatNS + "feedWithdrawalKept"
)

// SuppressedByReconcile is the PredSuppressedBy actor the auto-suppress
// reconciliation policy writes.
const SuppressedByReconcile = "feed-reconcile"

// WorkVisibility is one Work's projection stance.
type WorkVisibility struct {
	Tombstoned bool   `json:"tombstoned"`
	RedirectTo string `json:"redirectTo,omitempty"` // successor Work id, when named
	Suppressed bool   `json:"suppressed"`
	// Withdrawn is the date the feed reconciliation flagged this Work as
	// gone from its sole bib feed ("" = not withdrawn).
	Withdrawn string `json:"withdrawn,omitempty"`
	// SuppressedBy names what set the suppression ("" = a curator).
	SuppressedBy string `json:"suppressedBy,omitempty"`
	// Kept records a curator's decision to keep the Work despite withdrawal.
	Kept bool `json:"kept,omitempty"`
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
		case PredWithdrawn:
			v.Withdrawn = q.O.Value
		case PredSuppressedBy:
			v.SuppressedBy = q.O.Value
		case PredFeedKept:
			v.Kept = q.O.Value == "true"
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

// SetSuppressed hides or unhides a Work from projection. Unsuppressing also
// drops any PredSuppressedBy provenance.
func SetSuppressed(grainNQ []byte, workID string, suppressed bool) ([]byte, error) {
	if !suppressed {
		grainNQ, err := replaceWorkStatement(grainNQ, workID, PredSuppressed, nil)
		if err != nil {
			return nil, err
		}
		return replaceWorkStatement(grainNQ, workID, PredSuppressedBy, nil)
	}
	object := rdf.NewLiteral("true", "", "")
	return replaceWorkStatement(grainNQ, workID, PredSuppressed, &object)
}

// SetSuppressedBy records what set a Work's suppression.
func SetSuppressedBy(grainNQ []byte, workID, actor string) ([]byte, error) {
	object := rdf.NewLiteral(actor, "", "")
	return replaceWorkStatement(grainNQ, workID, PredSuppressedBy, &object)
}

// SetWithdrawn flags a Work as gone from its sole bib feed on the given ISO
// date; ClearWithdrawn removes the flag.
func SetWithdrawn(grainNQ []byte, workID, date string) ([]byte, error) {
	object := rdf.NewLiteral(date, "", "")
	return replaceWorkStatement(grainNQ, workID, PredWithdrawn, &object)
}

// ClearWithdrawn removes a Work's withdrawal flag.
func ClearWithdrawn(grainNQ []byte, workID string) ([]byte, error) {
	return replaceWorkStatement(grainNQ, workID, PredWithdrawn, nil)
}

// SetFeedKept records (or clears) a curator's keep-despite-withdrawal
// decision.
func SetFeedKept(grainNQ []byte, workID string, kept bool) ([]byte, error) {
	if !kept {
		return replaceWorkStatement(grainNQ, workID, PredFeedKept, nil)
	}
	object := rdf.NewLiteral("true", "", "")
	return replaceWorkStatement(grainNQ, workID, PredFeedKept, &object)
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
