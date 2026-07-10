package httpapi

import (
	"context"
	"net/http"
	"strconv"
	"sync"

	"github.com/freeeve/libcat/ingest"
	"github.com/freeeve/libcat/similar"

	"github.com/freeeve/libcat/backend/auth"
	"github.com/freeeve/libcat/backend/vocab"
	"github.com/freeeve/libcat/backend/workindex"
)

// defaultSimilarLimit matches the OPAC's precomputed rail, so the panel a
// cataloger sees while editing is the rail a reader will get after the next
// publish. maxSimilarLimit bounds a hostile ?limit=.
const (
	defaultSimilarLimit = 8
	maxSimilarLimit     = 50
)

// similarIndex caches the built similarity index against the work index's
// generation.
//
// Building is O(corpus): ~26 ms and ~69 MB at 62,602 works. Per query it is
// ~0.6 ms, which is cheaper than the grain read the editor is already doing. So
// build once per corpus change, not once per request -- and never on a timer,
// because an editor who has just re-subjected a work wants the neighbours to move
// now, and the works list already owns that freshness contract.
type similarIndex struct {
	wix  *workindex.Index
	opts func() similar.Options

	mu    sync.Mutex
	gen   uint64
	built *builtSimilar
}

// builtSimilar is the scorer plus the two things it deliberately does not carry:
// the titles a panel renders, and which work ids exist at all. similar.Work holds
// only what is scored, and a tombstoned work is scored by nothing yet still exists.
type builtSimilar struct {
	ix     *similar.Index
	titles map[string]string
	known  map[string]bool
}

// get returns an index over the current corpus, rebuilding only when the work
// index's derived views have changed since the last build.
func (s *similarIndex) get(ctx context.Context) (*builtSimilar, error) {
	summaries, gen, err := s.wix.SummariesWithGeneration(ctx)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.built != nil && s.gen == gen {
		return s.built, nil
	}
	b := &builtSimilar{
		ix:     similar.Build(ingest.SimilarWorks(summaries), s.opts()),
		titles: make(map[string]string, len(summaries)),
		known:  make(map[string]bool, len(summaries)),
	}
	for _, sm := range summaries {
		b.titles[sm.WorkID] = sm.Title
		b.known[sm.WorkID] = true
	}
	s.built, s.gen = b, gen
	return b, nil
}

// vocabTree adapts the installed vocabularies into the scorer's concept-tree
// hooks. Nil when no vocabulary is installed, which disables the tree walk and
// leaves the walk scoring on subject IRIs alone.
//
// This is the one place the admin's neighbours can legitimately differ from the
// OPAC's: the projector reads skos:broader out of catalog.json's own term
// sideband, while the editor reads the vocabulary snapshots installed here. They
// agree when the enrichers that wrote the graph read the same vocabulary, which is
// the normal case and not an invariant this code can assert.
func vocabTree(vx *vocab.Index) (broader, narrower func(string) []string) {
	if vx == nil {
		return nil, nil
	}
	edges := func(pick func(*vocab.Term) []string) func(string) []string {
		return func(iri string) []string {
			if t, ok := vx.Resolve(iri); ok {
				return pick(t)
			}
			return nil
		}
	}
	return edges(func(t *vocab.Term) []string { return t.Broader }),
		edges(func(t *vocab.Term) []string { return t.Narrower })
}

// registerWorksSimilar mounts GET /v1/works/{id}/similar?limit= (librarian).
//
// The same scorer the OPAC's build step runs, over the admin corpus rather than
// the projection -- so a cataloger sees the rail their edits are about to produce.
// Two differences follow from the corpus, not from the code: suppressed works
// appear here and never in the projection, and the concept tree comes from the
// installed vocabularies rather than the projected term sideband.
//
// A work the index does not know is a 404. A work that scores nothing is a 200
// with an empty list: "this record resembles nothing you hold" is an answer, and
// on a thinly-subjected record it is the expected one.
func registerWorksSimilar(mux *http.ServeMux, ix *workindex.Index, verifier auth.TokenVerifier, vx *vocab.Index) {
	cache := &similarIndex{wix: ix, opts: func() similar.Options {
		opts := similar.DefaultOptions()
		opts.Broader, opts.Narrower = vocabTree(vx)
		return opts
	}}
	librarian := auth.Require(verifier, auth.RoleLibrarian)

	mux.Handle("GET /v1/works/{id}/similar", librarian(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		workID := r.PathValue("id")
		if !workIDPattern.MatchString(workID) {
			writeError(w, http.StatusBadRequest, "bad work id")
			return
		}
		limit := defaultSimilarLimit
		if raw := r.URL.Query().Get("limit"); raw != "" {
			n, err := strconv.Atoi(raw)
			if err != nil || n <= 0 || n > maxSimilarLimit {
				writeError(w, http.StatusBadRequest, "limit must be 1.."+strconv.Itoa(maxSimilarLimit))
				return
			}
			limit = n
		}
		b, err := cache.get(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, "scan failed")
			return
		}
		if !b.known[workID] {
			writeError(w, http.StatusNotFound, "no such work")
			return
		}
		// A tombstoned work is known but absent from the index, so Neighbors
		// returns nothing and this is a 200 with an empty list -- which is the
		// true answer, and distinguishable from a typo'd id above.
		scored := b.ix.Neighbors(workID, limit)
		out := make([]similarNeighbor, 0, len(scored))
		for _, s := range scored {
			out = append(out, similarNeighbor{
				WorkID: s.WorkID,
				Title:  b.titles[s.WorkID],
				Score:  s.Score,
				Shared: s.Shared,
			})
		}
		writeJSON(w, http.StatusOK, map[string]any{"similar": out})
	})))
}

// similarNeighbor is the wire shape. Score rides along here and not in the OPAC's
// sidecar: a cataloger asking "why is this here?" is the one reader for whom the
// number means something.
type similarNeighbor struct {
	WorkID string   `json:"workId"`
	Title  string   `json:"title"`
	Score  float64  `json:"score"`
	Shared []string `json:"shared,omitempty"`
}
