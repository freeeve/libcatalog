package bibframe

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/freeeve/libcat/identity"
	"github.com/freeeve/libcodex/rdf"
)

// LcatNS is libcat's own namespace for statements BIBFRAME 2.0 does not cover
// (ARCHITECTURE §5, "extend, don't fight the model"). Its clustering-correction
// predicates live in the editorial graph, so they are preserved across re-ingest
// and the computed clustering key cannot override a human decision (§4).
const LcatNS = "https://github.com/freeeve/libcat/ns#"

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

// workNodePattern matches exactly the IRI WorkIRI mints, and nothing that
// merely starts like it: an editorial skolem node ("#<id>Work-ed-title") names
// a title node, not the Work.
var workNodePattern = regexp.MustCompile(`^#(w[a-z0-9]{6,20})Work$`)

// WorkIDFromIRI recovers the Work id a grain-local Work node names. It is what
// lets a statement about one Work be rebound to another (a batch edit): every
// other grain-local node -- instances, skolem children -- names something that
// exists in one grain and nowhere else, so it cannot be rebound at all.
func WorkIDFromIRI(iri string) (string, bool) {
	m := workNodePattern.FindStringSubmatch(iri)
	if m == nil {
		return "", false
	}
	return m[1], true
}

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

// ScanMergesDataset is ScanMerges for callers that already hold the parsed
// dataset (the work index scans everything off one parse).
func ScanMergesDataset(ds *rdf.Dataset) []identity.Merge { return datasetMerges(ds) }

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
// (re-canonicalized). Thin wrapper over ApplyEditorialPatch.
func AddMergeMarker(grainNQ []byte, from, to string) ([]byte, error) {
	return ApplyEditorialPatch(grainNQ, Patch{Add: []rdf.Quad{{
		S: rdf.NewIRI(WorkIRI(from)),
		P: rdf.NewIRI(PredMergedInto),
		O: rdf.NewIRI(WorkIRI(to)),
	}}})
}

// ScanPins recovers the editorial split pins in one grain's N-Quads: every
// lcat:workAssignment statement in the editorial graph, as Instance->Work id
// pairs. A grain with none yields nil.
func ScanPins(nq []byte) ([]identity.Pin, error) {
	ds, err := rdf.ParseNQuads(nq)
	if err != nil {
		return nil, err
	}
	return scanPinsDataset(ds), nil
}

// scanPinsDataset extracts the editorial-graph lcat:workAssignment pairs from
// a parsed dataset.
func scanPinsDataset(ds *rdf.Dataset) []identity.Pin {
	ed := EditorialGraph()
	var out []identity.Pin
	for _, q := range ds.Quads {
		if q.G == ed && q.P.Value == PredWorkAssignment && q.S.IsIRI() && q.O.IsIRI() {
			out = append(out, identity.Pin{Instance: FragInstance(q.S.Value), Work: fragWork(q.O.Value)})
		}
	}
	return out
}

// AddSplitMarkers records an editorial split in a grain's N-Quads: a
// lcat:splitFrom provenance statement (newWork split off fromWork) plus one
// lcat:workAssignment pin per Instance, all in the editorial graph, and returns the
// re-canonicalized grain. Adding a marker that already exists is a no-op, so it is
// idempotent. Recorded in the source Work's grain, the pins are recovered on the
// next ingest and force the pinned Instances onto newWork. Thin wrapper over
// ApplyEditorialPatch.
// Every pinned Instance must be one the grain describes: a pin is a
// permanent instruction to the identity resolver (SeedPin), so a typo or an
// id from another record would silently strand that Instance on a work id
// with no grain at the next ingest (the no-phantom-ids invariant).
func AddSplitMarkers(grainNQ []byte, newWork, fromWork string, instanceIDs []string) ([]byte, error) {
	ds, err := rdf.ParseNQuads(grainNQ)
	if err != nil {
		return nil, err
	}
	for _, inst := range instanceIDs {
		subject := rdf.NewIRI(InstanceIRI(inst))
		described := false
		for i := range ds.Quads {
			if ds.Quads[i].S == subject {
				described = true
				break
			}
		}
		if !described {
			return nil, fmt.Errorf("bibframe: %w: no instance %s in this grain", ErrNoSuchInstance, inst)
		}
	}
	patch := Patch{Add: []rdf.Quad{{
		S: rdf.NewIRI(WorkIRI(newWork)),
		P: rdf.NewIRI(PredSplitFrom),
		O: rdf.NewIRI(WorkIRI(fromWork)),
	}}}
	for _, inst := range instanceIDs {
		patch.Add = append(patch.Add, rdf.Quad{
			S: rdf.NewIRI(InstanceIRI(inst)),
			P: rdf.NewIRI(PredWorkAssignment),
			O: rdf.NewIRI(WorkIRI(newWork)),
		})
	}
	return ApplyEditorialPatch(grainNQ, patch)
}

// SplitTargetFor returns the Work id a prior split already assigned to exactly the
// given instances in this grain, or "" if there is none. A retried or double-clicked
// split reuses that id instead of minting a fresh one, so AddSplitMarkers finds every
// marker already present and no-ops -- the endpoint is idempotent, and the grain never
// ends up with two workAssignment pins for one instance.
//
// The match is on the exact instance set: a split of [i1] and a later split of
// [i1, i2] are different operations, not a retry of the first, so the second mints its
// own Work rather than folding i2 into the first split. Only when the requested set is
// precisely the set some earlier split already pinned to one Work is that Work reused.
func SplitTargetFor(grainNQ []byte, instanceIDs []string) (string, error) {
	pins, err := ScanPins(grainNQ)
	if err != nil {
		return "", err
	}
	want := make(map[string]bool, len(instanceIDs))
	for _, inst := range instanceIDs {
		want[inst] = true
	}
	byWork := map[string]map[string]bool{}
	for _, p := range pins {
		if byWork[p.Work] == nil {
			byWork[p.Work] = map[string]bool{}
		}
		byWork[p.Work][p.Instance] = true
	}
	for work, insts := range byWork {
		if len(insts) != len(want) {
			continue
		}
		matches := true
		for inst := range want {
			if !insts[inst] {
				matches = false
				break
			}
		}
		if matches {
			return work, nil
		}
	}
	return "", nil
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

// FragInstance strips the "#" prefix and "Instance" suffix from an Instance node
// IRI, the inverse of InstanceIRI.
func FragInstance(iri string) string {
	return strings.TrimSuffix(strings.TrimPrefix(iri, "#"), "Instance")
}
