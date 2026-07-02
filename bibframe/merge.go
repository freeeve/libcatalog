package bibframe

import (
	"sort"
	"strings"

	"github.com/freeeve/libcatalog/identity"
	"github.com/freeeve/libcodex/rdf"
)

// LcatNS is libcatalog's own namespace for statements BIBFRAME 2.0 does not cover
// (ARCHITECTURE §5, "extend, don't fight the model"). Its clustering-correction
// predicates live in the editorial graph, so they are preserved across re-ingest
// and the computed clustering key cannot override a human decision (§4).
const LcatNS = "https://github.com/freeeve/libcatalog/ns#"

// PredMergedInto records an editorial merge: the retired Work (subject) was merged
// into the surviving Work (object). It is the under-merge fix -- two records that
// should be one Work but clustered apart. The retired Work's grain is dropped on
// the next ingest (its Instances resolve onto the survivor); the projector emits a
// redirect so the retired id's URL survives.
const PredMergedInto = LcatNS + "mergedInto"

// PredSplitFrom records the provenance of an editorial split: the new Work
// (subject) was split off from the original over-merged Work (object). It pairs
// with per-Instance PredWorkAssignment pins that force the split to reproduce on
// re-ingest even though the computed key still clusters the Instances together.
const PredSplitFrom = LcatNS + "splitFrom"

// PredWorkAssignment pins an Instance (subject) to a specific Work (object),
// overriding the computed clustering key -- the mechanism that makes an editorial
// split reproducible. It lives in the editorial graph, so the pin is preserved
// across ingest.
const PredWorkAssignment = LcatNS + "workAssignment"

// WorkIRI returns the node IRI libcodex mints for a Work id (the "#<id>Work"
// fragment the grains use), so editorial overlay statements reference the same
// node the feed does.
func WorkIRI(id string) string { return "#" + id + "Work" }

// InstanceIRI returns the node IRI libcodex mints for an Instance id.
func InstanceIRI(id string) string { return "#" + id + "Instance" }

// ScanMerges recovers the editorial merge decisions in one grain's N-Quads: every
// lcat:mergedInto statement in the editorial graph, as From->To Work-id pairs. A
// grain with none yields nil.
func ScanMerges(nq []byte) ([]identity.Merge, error) {
	ds, err := rdf.ParseNQuads(nq)
	if err != nil {
		return nil, err
	}
	return datasetMerges(ds), nil
}

// datasetMerges extracts the editorial-graph lcat:mergedInto pairs from a parsed
// dataset.
func datasetMerges(ds *rdf.Dataset) []identity.Merge {
	ed := EditorialGraph()
	var out []identity.Merge
	for _, q := range ds.Quads {
		if q.G == ed && q.P.Value == PredMergedInto && q.S.IsIRI() && q.O.IsIRI() {
			out = append(out, identity.Merge{From: fragWork(q.S.Value), To: fragWork(q.O.Value)})
		}
	}
	return out
}

// AddMergeMarker adds the editorial lcat:mergedInto statement (from -> to) to a
// Work grain's N-Quads and returns the re-canonicalized grain, so the decision is
// recorded durably in the survivor's grain and preserved across re-ingest. It is
// idempotent: a grain that already records this merge is returned unchanged
// (re-canonicalized).
func AddMergeMarker(grainNQ []byte, from, to string) ([]byte, error) {
	ds, err := rdf.ParseNQuads(grainNQ)
	if err != nil {
		return nil, err
	}
	s := rdf.NewIRI(WorkIRI(from))
	p := rdf.NewIRI(PredMergedInto)
	o := rdf.NewIRI(WorkIRI(to))
	ed := EditorialGraph()
	for _, q := range ds.Quads {
		if q.G == ed && q.S == s && q.P == p && q.O == o {
			return ds.Canonical()
		}
	}
	ds.Add(s, p, o, ed)
	return ds.Canonical()
}

// ScanPins recovers the editorial split pins in one grain's N-Quads: every
// lcat:workAssignment statement in the editorial graph, as Instance->Work id
// pairs. A grain with none yields nil.
func ScanPins(nq []byte) ([]identity.Pin, error) {
	ds, err := rdf.ParseNQuads(nq)
	if err != nil {
		return nil, err
	}
	ed := EditorialGraph()
	var out []identity.Pin
	for _, q := range ds.Quads {
		if q.G == ed && q.P.Value == PredWorkAssignment && q.S.IsIRI() && q.O.IsIRI() {
			out = append(out, identity.Pin{Instance: fragInstance(q.S.Value), Work: fragWork(q.O.Value)})
		}
	}
	return out, nil
}

// AddSplitMarkers records an editorial split in a grain's N-Quads: a
// lcat:splitFrom provenance statement (newWork split off fromWork) plus one
// lcat:workAssignment pin per Instance, all in the editorial graph, and returns the
// re-canonicalized grain. Adding a marker that already exists is a no-op, so it is
// idempotent. Recorded in the source Work's grain, the pins are recovered on the
// next ingest and force the pinned Instances onto newWork.
func AddSplitMarkers(grainNQ []byte, newWork, fromWork string, instanceIDs []string) ([]byte, error) {
	ds, err := rdf.ParseNQuads(grainNQ)
	if err != nil {
		return nil, err
	}
	ed := EditorialGraph()
	addUnique(ds, rdf.NewIRI(WorkIRI(newWork)), rdf.NewIRI(PredSplitFrom), rdf.NewIRI(WorkIRI(fromWork)), ed)
	for _, inst := range instanceIDs {
		addUnique(ds, rdf.NewIRI(InstanceIRI(inst)), rdf.NewIRI(PredWorkAssignment), rdf.NewIRI(WorkIRI(newWork)), ed)
	}
	return ds.Canonical()
}

// addUnique appends a quad to the dataset only if it is not already present.
func addUnique(ds *rdf.Dataset, s, p, o, g rdf.Term) {
	for _, q := range ds.Quads {
		if q.S == s && q.P == p && q.O == o && q.G == g {
			return
		}
	}
	ds.Add(s, p, o, g)
}

// RetiredWorks returns the sorted, distinct retired Work ids among merges (the From
// side of every merge). Their grains are removed on ingest once their Instances
// have moved to the survivor.
func RetiredWorks(merges []identity.Merge) []string {
	set := map[string]bool{}
	for _, m := range merges {
		if m.From != "" && m.From != m.To {
			set[m.From] = true
		}
	}
	out := make([]string, 0, len(set))
	for id := range set {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// fragWork strips the "#" prefix and "Work" suffix from a Work node IRI, the
// inverse of WorkIRI.
func fragWork(iri string) string {
	return strings.TrimSuffix(strings.TrimPrefix(iri, "#"), "Work")
}

// fragInstance strips the "#" prefix and "Instance" suffix from an Instance node
// IRI, the inverse of InstanceIRI.
func fragInstance(iri string) string {
	return strings.TrimSuffix(strings.TrimPrefix(iri, "#"), "Instance")
}
