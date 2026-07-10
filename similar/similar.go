// Package similar answers "more like this" over a catalog: given a Work, which
// other Works does it most resemble (tasks/284)?
//
// The method is a two-hop walk over the bipartite graph of Works and their
// attributes -- series, contributors, tags, subjects. From the focus Work, step
// out to each attribute it carries, then back in to every other Work carrying
// that attribute. Each shared attribute contributes to that Work's score.
//
// Three things keep the walk honest, and none of them is optional:
//
//   - Rarity weighting. A shared attribute is worth weight/log2(df+2), where df
//     is how many Works carry it. Two books sharing an obscure subject heading
//     have told you far more than two books sharing "Fiction".
//   - A document-frequency cap. An attribute held by more than DFCapFraction of
//     the catalog is skipped entirely: it cannot discriminate. The cap is a
//     fraction rather than a constant so it scales with the collection.
//   - A singleton floor. An attribute only this Work carries links to nothing,
//     and one shared by exactly two Works is the most informative case there is,
//     so the floor is df >= 2 rather than any higher threshold.
//
// Subjects additionally walk the SKOS concept tree: a Work's subjects are
// expanded upward through skos:broader (decayed per hop) and one step down
// through narrower. This is what makes the result feel like subject cataloging
// rather than string matching -- "Lesbian mothers" and "Lesbian parents" are
// neighbours in the tree even when no Work carries both IRIs.
//
// Language is scored differently on purpose. It is a flat bonus applied only to
// Works some other signal has already scored, never a way onto the list: every
// Work shares a language with most of the catalog, so as a walk edge it would
// return the whole catalog in an arbitrary order.
//
// The package is pure. It holds no store, no HTTP, no Hugo -- so the OPAC's
// build step and the admin's live endpoint can compute identical neighbours
// instead of drifting apart.
package similar

import (
	"math"
	"slices"
	"sort"

	"github.com/freeeve/libcat/ingest"
)

// Relation names the attribute kinds the walk traverses.
type Relation int

const (
	RelSeries Relation = iota
	RelContributor
	RelTag
	RelSubject
	numRelations
)

// Weights are the per-relation contributions, before rarity weighting. The
// defaults are qllpoc's, tuned against a 62k-work public-library catalog: two
// books in a series are related whatever else they share, a shared author is
// strong, a shared uncontrolled tag is suggestive, and a single shared subject
// heading is the weakest real evidence there is.
type Weights [numRelations]float64

// DefaultWeights is the shipped tuning. Raising Tag lets one coincidental
// folksonomy label float unrelated books onto the rail; that is the failure mode
// this ordering exists to avoid.
var DefaultWeights = Weights{
	RelSeries:      5,
	RelContributor: 3,
	RelTag:         2,
	RelSubject:     1,
}

// Options tunes the walk. The zero value is not useful; use DefaultOptions.
type Options struct {
	Weights Weights
	// DFCapFraction skips any attribute carried by more than this fraction of
	// the catalog. 0.20 means "a subject on a fifth of the catalog tells us
	// nothing". Values <= 0 or >= 1 disable the cap.
	DFCapFraction float64
	// LanguageBonus is added once to any candidate that shares a language with
	// the focus Work and was already scored by another signal.
	LanguageBonus float64
	// AvailabilityBonus nudges a holdable candidate above an identical one that
	// the reader cannot borrow.
	AvailabilityBonus float64
	// TreeDepth is how many skos:broader hops a subject expands upward;
	// TreeDecay multiplies the contribution per hop. Depth 0 disables the tree
	// walk. Narrower is always exactly one hop, at TreeDecay.
	TreeDepth int
	TreeDecay float64
	// Broader and Narrower resolve a subject IRI's neighbours in the concept
	// tree. Both may be nil, which disables the tree walk. They exist as hooks
	// so this package never imports a vocabulary index.
	Broader  func(iri string) []string
	Narrower func(iri string) []string
}

// DefaultOptions carries qllpoc's tuning.
func DefaultOptions() Options {
	return Options{
		Weights:           DefaultWeights,
		DFCapFraction:     0.20,
		LanguageBonus:     20,
		AvailabilityBonus: 0.3,
		TreeDepth:         2,
		TreeDecay:         0.5,
	}
}

// Scored is one neighbour and why it ranked.
type Scored struct {
	WorkID string  `json:"workId"`
	Score  float64 `json:"score"`
	// Shared names the attributes that put this Work on the list, most
	// valuable first, so a cataloger asking "why is this here?" has an answer.
	Shared []string `json:"shared,omitempty"`
}

// Index is a built, read-only postings index. Safe for concurrent Neighbors.
type Index struct {
	opts  Options
	works []ingest.WorkSummary
	byID  map[string]int
	// postings[rel][value] lists the work offsets carrying that value.
	postings [numRelations]map[string][]int
	// langs[code] is unused as a posting list; language is a bonus, not an edge.
	langs [][]string
	dfCap int
}

// attrsOf returns the Work's values for one relation.
func attrsOf(s ingest.WorkSummary, rel Relation) []string {
	switch rel {
	case RelSeries:
		return s.Series
	case RelContributor:
		return s.Contributors
	case RelTag:
		return s.Tags
	case RelSubject:
		return s.Subjects
	}
	return nil
}

// Build indexes the catalog. Tombstoned Works are excluded outright: a retired
// record must not be recommended from elsewhere, and it has no neighbours of its
// own (tasks/280). Suppressed Works are kept -- the admin surface shows them,
// and the public projection never sees them because it drops them upstream.
// A repeated WorkID is indexed once, at its first occurrence. Duplicates are a
// caller error -- ScanSummaries over a prefix that also catches catalog.nq will
// produce them -- but the failure they cause is nasty and silent: the focus Work
// occupies several offsets, Neighbors excludes only the one it looked up, and the
// Work recommends itself at the top of its own rail.
func Build(works []ingest.WorkSummary, opts Options) *Index {
	ix := &Index{opts: opts, byID: make(map[string]int, len(works))}
	for _, w := range works {
		if w.Tombstoned {
			continue
		}
		if _, dup := ix.byID[w.WorkID]; dup {
			continue
		}
		ix.byID[w.WorkID] = len(ix.works)
		ix.works = append(ix.works, w)
		ix.langs = append(ix.langs, w.Languages)
	}
	for rel := range numRelations {
		ix.postings[rel] = map[string][]int{}
	}
	for i, w := range ix.works {
		for rel := range numRelations {
			for _, v := range attrsOf(w, Relation(rel)) {
				ix.postings[rel][v] = append(ix.postings[rel][v], i)
			}
		}
	}
	ix.dfCap = len(ix.works) + 1 // no cap
	if opts.DFCapFraction > 0 && opts.DFCapFraction < 1 {
		// The cap must never exclude df=2. An attribute shared by exactly two
		// Works is the most informative evidence the walk can find, and on a
		// small catalog the fraction alone would round it away -- floor(0.2*5)
		// is 1, which would leave a five-book catalog with no neighbours at all.
		ix.dfCap = max(2, int(math.Floor(opts.DFCapFraction*float64(len(ix.works)))))
	}
	return ix
}

// Len reports how many Works the index scores over.
func (ix *Index) Len() int { return len(ix.works) }

// idf is the rarity weight of an attribute carried by df Works. Rarer is worth
// more, and the +2 keeps df=1 from dividing by zero even though the floor
// already excludes it.
func idf(df int) float64 { return 1 / math.Log2(float64(df)+2) }

// usable reports whether an attribute links the focus Work to anyone, and is not
// so common it describes the catalog rather than the book.
//
// The floor is "at least one Work other than the focus", not qllpoc's df >= 2.
// The two agree on an attribute the focus carries, where df counts the focus
// itself. They disagree on a tree-expanded concept, which the focus does not
// carry: there df == 1 means exactly one *other* Work sits under that concept,
// which is a match, and qllpoc drops it. On a small catalog that is the
// difference between a rail and an empty box.
func (ix *Index) usable(others, df int) bool { return others >= 1 && df <= ix.dfCap }

// scorer accumulates one query's candidate scores.
type scorer struct {
	ix     *Index
	focus  int
	score  map[int]float64
	shared map[int][]sharedAttr
}

type sharedAttr struct {
	value  string
	weight float64
}

// contribute walks out to one attribute value and back in to its other Works.
func (sc *scorer) contribute(rel Relation, value string, weight float64) {
	works := sc.ix.postings[rel][value]
	others := len(works)
	if slices.Contains(works, sc.focus) {
		others--
	}
	if !sc.ix.usable(others, len(works)) {
		return
	}
	// Rarity is measured over the whole catalog, so df counts the focus: a
	// heading on 400 Works is common whether or not this is one of them.
	w := weight * idf(len(works))
	for _, other := range works {
		if other == sc.focus {
			continue
		}
		sc.score[other] += w
		sc.shared[other] = append(sc.shared[other], sharedAttr{value: value, weight: w})
	}
}

// expandSubjects walks the concept tree around the focus Work's subjects,
// returning each reachable IRI with the weight multiplier it earned. The focus
// Work's own subjects sit at 1; each broader hop decays; narrower is one hop.
//
// Breadth-first with a visited set, so a diamond in the hierarchy (two parents
// sharing a grandparent) contributes once, at its best weight, rather than
// compounding.
func (ix *Index) expandSubjects(subjects []string) map[string]float64 {
	weights := make(map[string]float64, len(subjects))
	for _, iri := range subjects {
		weights[iri] = 1
	}
	if ix.opts.TreeDepth <= 0 || ix.opts.Broader == nil {
		return weights
	}
	frontier := append([]string(nil), subjects...)
	w := 1.0
	for hop := 0; hop < ix.opts.TreeDepth && len(frontier) > 0; hop++ {
		w *= ix.opts.TreeDecay
		var next []string
		for _, iri := range frontier {
			for _, up := range ix.opts.Broader(iri) {
				if w > weights[up] {
					weights[up] = w
					next = append(next, up)
				}
			}
		}
		frontier = next
	}
	if ix.opts.Narrower != nil {
		for _, iri := range subjects {
			for _, down := range ix.opts.Narrower(iri) {
				if d := ix.opts.TreeDecay; d > weights[down] {
					weights[down] = d
				}
			}
		}
	}
	return weights
}

// Neighbors returns up to n Works most similar to workID, best first. An unknown
// or tombstoned workID yields nothing rather than an error: it has no neighbours,
// which is the honest answer and the one every caller would otherwise write.
func (ix *Index) Neighbors(workID string, n int) []Scored {
	focus, ok := ix.byID[workID]
	if !ok || n <= 0 {
		return nil
	}
	sc := &scorer{ix: ix, focus: focus, score: map[int]float64{}, shared: map[int][]sharedAttr{}}
	me := ix.works[focus]

	for _, rel := range []Relation{RelSeries, RelContributor, RelTag} {
		for _, v := range attrsOf(me, rel) {
			sc.contribute(rel, v, ix.opts.Weights[rel])
		}
	}
	for iri, mult := range ix.expandSubjects(me.Subjects) {
		sc.contribute(RelSubject, iri, ix.opts.Weights[RelSubject]*mult)
	}

	// Language and availability are bonuses on an already-scored candidate, never
	// a way onto the list. Applied after the walk for exactly that reason.
	mine := map[string]bool{}
	for _, l := range me.Languages {
		mine[l] = true
	}
	for i := range sc.score {
		for _, l := range ix.langs[i] {
			if mine[l] {
				sc.score[i] += ix.opts.LanguageBonus
				break
			}
		}
		if ix.works[i].HasAvailability || ix.works[i].Items > 0 {
			sc.score[i] += ix.opts.AvailabilityBonus
		}
	}
	return ix.rank(sc, n)
}

// rank orders candidates by score, breaking ties by work id so the same catalog
// always yields the same rail -- a build step that reshuffled its output on every
// run would churn the OPAC's pages for nothing.
func (ix *Index) rank(sc *scorer, n int) []Scored {
	out := make([]Scored, 0, len(sc.score))
	for i, score := range sc.score {
		out = append(out, Scored{WorkID: ix.works[i].WorkID, Score: score, Shared: topShared(sc.shared[i])})
	}
	sort.Slice(out, func(a, b int) bool {
		if out[a].Score != out[b].Score {
			return out[a].Score > out[b].Score
		}
		return out[a].WorkID < out[b].WorkID
	})
	if len(out) > n {
		out = out[:n]
	}
	return out
}

// maxShared bounds the explanation, not the score.
const maxShared = 5

// topShared names the attributes that contributed most, best first.
func topShared(attrs []sharedAttr) []string {
	sort.SliceStable(attrs, func(a, b int) bool { return attrs[a].weight > attrs[b].weight })
	out := make([]string, 0, min(len(attrs), maxShared))
	seen := map[string]bool{}
	for _, a := range attrs {
		if seen[a.value] {
			continue
		}
		seen[a.value] = true
		out = append(out, a.value)
		if len(out) == maxShared {
			break
		}
	}
	return out
}
