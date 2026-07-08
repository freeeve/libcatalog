package httpapi

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/freeeve/libcat/ingest"

	"github.com/freeeve/libcat/backend/auth"
	"github.com/freeeve/libcat/backend/workindex"
)

// worksList serves the SPA's work search off the shared work index, which
// carries the freshness contract this cache used to own (fresh after a
// publish, not per-keystroke scans).
type worksList struct {
	ix *workindex.Index
}

func (wl *worksList) summaries(r *http.Request) ([]ingest.WorkSummary, error) {
	return wl.ix.Summaries(r.Context())
}

// registerWorksList mounts GET /v1/works?q=&limit=&offset= (librarian) and
// returns the shared summary source for sibling endpoints (tags typeahead).
// The response carries total (whole catalog) and matched (query hits) so
// the SPA can page: works is the [offset, offset+limit) window of matches.
func registerWorksList(mux *http.ServeMux, ix *workindex.Index, verifier auth.TokenVerifier) *worksList {
	wl := &worksList{ix: ix}
	librarian := auth.Require(verifier, auth.RoleLibrarian)
	mux.Handle("GET /v1/works", librarian(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		all, err := wl.summaries(r)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "scan failed")
			return
		}
		params := r.URL.Query()
		q := strings.ToLower(strings.TrimSpace(params.Get("q")))
		limit := 50
		if raw := params.Get("limit"); raw != "" {
			if n, err := strconv.Atoi(raw); err == nil && n > 0 && n <= 500 {
				limit = n
			}
		}
		offset := 0
		if raw := params.Get("offset"); raw != "" {
			if n, err := strconv.Atoi(raw); err == nil && n >= 0 {
				offset = n
			}
		}
		// Facets (tasks/168): filters AND across groups, OR within one;
		// counts are self-excluding over the query-matched set.
		filters := parseWorkFilters(params)
		counter := newFacetCounter()
		matched := 0
		matches := make([]ingest.WorkSummary, 0, limit)
		for _, s := range all {
			if q != "" && !s.Matches(q) {
				continue
			}
			m := filters.groupMatches(s)
			counter.add(s, m)
			pass := true
			for _, ok := range m {
				pass = pass && ok
			}
			if !pass {
				continue
			}
			matched++
			if matched <= offset || len(matches) >= limit {
				continue
			}
			matches = append(matches, s)
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"works":   matches,
			"total":   len(all),
			"matched": matched,
			"offset":  offset,
			"facets":  counter.result(),
		})
	})))
	return wl
}
